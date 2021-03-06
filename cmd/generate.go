package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v4"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"

	"github.com/stack11/trek/internal"
)

const (
	regexpPartialLowerKebabCase = `[a-z][a-z0-9\-]*[a-z]`
)

//nolint:gocognit,cyclop
func NewGenerateCommand() *cobra.Command {
	var (
		dev       bool
		cleanup   bool
		overwrite bool
		stdout    bool
	)

	generateCmd := &cobra.Command{
		Use:   "generate [migration-name]",
		Short: "Generate the migrations for a pgModeler file",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			initializeConfig(cmd)

			return nil
		},
		Args: func(cmd *cobra.Command, args []string) error {
			if stdout {
				if len(args) != 0 {
					//nolint:goerr113
					return errors.New("pass no name for stdout generation")
				}
			} else {
				if len(args) == 0 {
					//nolint:goerr113
					return errors.New("pass the name of the migration")
				} else if len(args) > 1 {
					//nolint:goerr113
					return errors.New("expecting one migration name, use lower-kebab-case for the migration name")
				}

				var regexpLowerKebabCase = regexp.MustCompile(`^` + regexpPartialLowerKebabCase + `$`)
				if !regexpLowerKebabCase.MatchString(args[0]) {
					//nolint:goerr113
					return errors.New("migration name must be lower-kebab-case and must not start or end with a number or dash")
				}
			}

			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			config, err := internal.ReadConfig()
			if err != nil {
				log.Fatalf("Failed to read config: %v\n", err)
			}

			var runnerFunc RunnerFunc

			if stdout {
				var migrationsDir string
				migrationsDir, err = getMigrationsDir()
				if err != nil {
					log.Fatalf("Failed to get migrations directory: %v", err)
				}
				var migrationsCount uint
				migrationsCount, err = inspectMigrations(migrationsDir)
				if err != nil {
					log.Fatalf("Failed to inspect migrations directory: %v", err)
				}

				initial := migrationsCount == 0

				runnerFunc = func() error {
					return runWithStdout(config, initial)
				}
			} else {
				migrationName := args[0]
				var newMigrationFilePath string
				var migrationNumber uint
				newMigrationFilePath, migrationNumber, err = getNewMigrationFilePath(migrationName, overwrite)
				if err != nil {
					log.Fatalf("Failed to get new migration file path: %v\n", err)
				}

				defer func() {
					if dev && cleanup {
						if _, err = os.Stat(newMigrationFilePath); err == nil {
							err = os.Remove(newMigrationFilePath)
							if err != nil {
								log.Printf("failed to delete new migration file: %v\n", err)
							}
						}
					}
				}()

				runnerFunc = func() error {
					return runWithFile(config, newMigrationFilePath, migrationNumber)
				}
			}

			err = runnerFunc()
			if err != nil {
				log.Printf("Failed to run: %v\n", err)
			}

			if dev {
				for {
					time.Sleep(time.Millisecond * 100)
					err = runnerFunc()
					if err != nil {
						log.Printf("Failed to run: %v\n", err)
					}
				}
			}
		},
	}

	generateCmd.Flags().BoolVar(&dev, "dev", false, "Watch for file changes and automatically regenerate the migration file") //nolint:lll
	generateCmd.Flags().BoolVar(&cleanup, "cleanup", true, "Remove the generated migrations file. Only works with --dev")
	generateCmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite existing files")
	generateCmd.Flags().BoolVar(&stdout, "stdout", false, "Output migration statements to stdout")

	return generateCmd
}

func setupDatabase(name string, port uint32) (*embeddedpostgres.EmbeddedPostgres, *pgx.Conn) {
	postgres, dsn := internal.NewPostgresDatabase(fmt.Sprintf("/tmp/trek/%s", name), port)
	err := postgres.Start()
	if err != nil {
		log.Fatalf("Failed to start %s database: %v", name, err)
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		log.Fatalf("Unable to connect to %s database: %v", name, err)
	}

	return postgres, conn
}

type RunnerFunc = func() error

func runWithStdout(
	config *internal.Config,
	initial bool,
) error {
	updated, err := checkIfUpdated(config)
	if err != nil {
		return fmt.Errorf("failed to check if model has been updated: %w", err)
	}
	if updated {
		targetPostgres, targetConn := setupDatabase("target", 5432)
		defer func() {
			_ = targetConn.Close(context.Background())
			_ = targetPostgres.Stop()
		}()

		migratePostgres, migrateConn := setupDatabase("migrate", 5433)
		defer func() {
			_ = migrateConn.Close(context.Background())
			_ = migratePostgres.Stop()
		}()

		var statements string
		statements, err = generateMigrationStatements(
			config,
			initial,
			targetConn,
			migrateConn,
		)
		if err != nil && !errors.Is(err, ErrInvalidModel) {
			return fmt.Errorf("failed to generate migration statements: %w", err)
		}
		fmt.Println("")
		fmt.Println("--")
		fmt.Println(statements)
		fmt.Println("--")
	}

	return nil
}

func runWithFile(
	config *internal.Config,
	newMigrationFilePath string,
	migrationNumber uint,
) error {
	updated, err := checkIfUpdated(config)
	if err != nil {
		return fmt.Errorf("failed to check if model has been updated: %w", err)
	}
	if updated {
		if _, err = os.Stat(newMigrationFilePath); err == nil {
			err = os.Remove(newMigrationFilePath)
			if err != nil {
				return fmt.Errorf("failed to delete generated migration file: %w", err)
			}
		}

		targetPostgres, targetConn := setupDatabase("target", 5432)
		defer func() {
			_ = targetConn.Close(context.Background())
			_ = targetPostgres.Stop()
		}()

		migratePostgres, migrateConn := setupDatabase("migrate", 5433)
		defer func() {
			_ = migrateConn.Close(context.Background())
			_ = migratePostgres.Stop()
		}()

		var statements string
		statements, err = generateMigrationStatements(
			config,
			migrationNumber == 1,
			targetConn,
			migrateConn,
		)
		if err != nil && !errors.Is(err, ErrInvalidModel) {
			return fmt.Errorf("failed to generate migration statements: %w", err)
		}

		//nolint:gosec
		err = os.WriteFile(
			newMigrationFilePath,
			[]byte(statements),
			0o644,
		)
		if err != nil {
			return fmt.Errorf("failed to write migration file: %w", err)
		}
		log.Println("Wrote migration file")

		err = writeTemplateFiles(config, migrationNumber)
		if err != nil {
			return fmt.Errorf("failed to write template files: %w", err)
		}

		updated, err = generateDiffLockFile(newMigrationFilePath, targetConn, migrateConn)
		if err != nil {
			return fmt.Errorf("failed to generate diff lock file: %w", err)
		}

		if updated {
			log.Println("Wrote diff lock file")
		}
	}

	return nil
}

func checkIfUpdated(config *internal.Config) (bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return false, fmt.Errorf("failed to get working directory: %w", err)
	}

	m, err := os.ReadFile(filepath.Join(wd, fmt.Sprintf("%s.dbm", config.ModelName)))
	if err != nil {
		return false, fmt.Errorf("failed to read model file: %w", err)
	}
	mStr := strings.TrimSuffix(string(m), "\n")
	if mStr == "" || mStr == modelContent {
		return false, nil
	}
	modelContent = mStr

	log.Println("Changes detected")

	return true, nil
}

func generateDiffLockFile(newMigrationFilePath string, targetConn, migrateConn *pgx.Conn) (bool, error) {
	newMigrationFileContent, err := os.ReadFile(newMigrationFilePath)
	if err != nil {
		return false, fmt.Errorf("failed to read new migratio file: %w", err)
	}
	_, err = migrateConn.Exec(context.Background(), string(newMigrationFileContent))
	if err != nil {
		return false, fmt.Errorf("failed to apply generated migration: %w", err)
	}

	var diff string
	diff, err = diffSchemaDumps(targetConn, migrateConn)
	if err != nil {
		return false, fmt.Errorf("failed to diff schema dumps: %w", err)
	}

	var wd string
	wd, err = os.Getwd()
	if err != nil {
		return false, fmt.Errorf("failed to get working directory: %w", err)
	}

	lockfile := filepath.Join(wd, "diff.lock")
	hasStoredDiff := false
	var storedDiff string

	if _, err = os.Stat(lockfile); err == nil {
		hasStoredDiff = true
		var s []byte
		s, err = os.ReadFile(lockfile)
		if err != nil {
			return false, fmt.Errorf("failed to read diff.lock file: %w", err)
		}
		storedDiff = string(s)
	}

	if !hasStoredDiff || diff != storedDiff {
		err = os.WriteFile(lockfile, []byte(diff), 0o600)
		if err != nil {
			return false, fmt.Errorf("failed to write diff.lock file: %w", err)
		}

		return true, nil
	}

	return false, nil
}

func diffSchemaDumps(targetConn, migrateConn *pgx.Conn) (string, error) {
	pgDumpOptions := []string{
		"--schema-only",
		"--exclude-table=public.schema_migrations",
	}

	targetDump, err := internal.PgDump(internal.DSN(targetConn, "disable"), pgDumpOptions)
	if err != nil {
		//nolint:wrapcheck
		return "", err
	}

	migrateDump, err := internal.PgDump(internal.DSN(migrateConn, "disable"), pgDumpOptions)
	if err != nil {
		//nolint:wrapcheck
		return "", err
	}

	targetDumpFile := "/tmp/target.sql"
	err = os.WriteFile(targetDumpFile, []byte(cleanDump(targetDump)), 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to write target.sql file: %w", err)
	}

	migrateDumpFile := "/tmp/migrate.sql"
	err = os.WriteFile(migrateDumpFile, []byte(cleanDump(migrateDump)), 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to write migrate.sql file: %w", err)
	}

	gitDiffCmd := exec.Command(
		"git",
		"diff",
		migrateDumpFile,
		targetDumpFile,
	)
	gitDiffCmd.Stderr = os.Stderr

	output, err := gitDiffCmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if !(errors.As(err, &ee) && ee.ExitCode() <= 1) {
			return "", fmt.Errorf("failed to run git diff: %w %v", err, string(output))
		}
	}

	return string(output), nil
}

func cleanDump(dump string) string {
	var lines []string
	for _, line := range strings.Split(dump, "\n") {
		if line != "" && !strings.HasPrefix(line, "--") {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "\n")
}

func writeTemplateFiles(config *internal.Config, newVersion uint) error {
	for _, ts := range config.Templates {
		t, err := template.New(ts.Path).Parse(ts.Content)
		if err != nil {
			return fmt.Errorf("failed to parse template: %w", err)
		}

		dir := filepath.Dir(ts.Path)
		err = os.MkdirAll(dir, 0o755)
		if err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}

		f, err := os.Create(ts.Path)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", ts.Path, err)
		}

		err = t.Execute(f, map[string]interface{}{"NewVersion": newVersion})
		if err != nil {
			return fmt.Errorf("failed to execute template: %w", err)
		}
	}

	return nil
}

func getMigrationsDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}
	migrationsDir := filepath.Join(wd, "migrations")
	if _, err = os.Stat(migrationsDir); os.IsNotExist(err) {
		err = os.Mkdir(migrationsDir, 0o755)
		if err != nil {
			return "", fmt.Errorf("failed to create migrations directory: %w", err)
		}
	}

	return migrationsDir, nil
}

func getNewMigrationFilePath(migrationName string, overwrite bool) (path string, migrationNumber uint, err error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", 0, fmt.Errorf("failed to get working directory: %w", err)
	}
	migrationsDir, err := getMigrationsDir()
	if err != nil {
		return "", 0, fmt.Errorf("failed to get migrations directory: %w", err)
	}
	migrationsCount, err := inspectMigrations(migrationsDir)
	if err != nil {
		return "", 0, fmt.Errorf("failed to inspect migrations directory: %w", err)
	}

	var migrationsNumber uint
	if _, err = os.Stat(
		filepath.Join(wd, "migrations", getMigrationFileName(migrationsCount, migrationName)),
	); err == nil {
		if overwrite {
			migrationsNumber = migrationsCount
		} else {
			prompt := promptui.Prompt{
				//nolint:lll
				Label:     "The previous migration has the same name. Overwrite the previous migration instead of creating a new one",
				IsConfirm: true,
				Default:   "y",
			}
			if _, err = prompt.Run(); err == nil {
				migrationsNumber = migrationsCount
			} else {
				migrationsNumber = migrationsCount + 1
			}
		}
	} else {
		migrationsNumber = migrationsCount + 1
	}

	return filepath.Join(wd, "migrations", getMigrationFileName(migrationsNumber, migrationName)), migrationsNumber, nil
}

func getMigrationFileName(migrationNumber uint, migrationName string) string {
	return fmt.Sprintf("%03d_%s.up.sql", migrationNumber, migrationName)
}

var (
	//nolint:gochecknoglobals
	modelContent = ""
)

func exportToPng(config *internal.Config, wd string) {
	err := internal.PgModelerExportToPng(
		filepath.Join(wd, fmt.Sprintf("%s.dbm", config.ModelName)),
		filepath.Join(wd, fmt.Sprintf("%s.png", config.ModelName)),
	)
	if err != nil {
		log.Println(err)
	}
}

var ErrInvalidModel = errors.New("invalid model")

//nolint:cyclop
func generateMigrationStatements(
	config *internal.Config,
	initial bool,
	targetConn,
	migrateConn *pgx.Conn,
) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	log.Println("Generating migration statements")

	go exportToPng(config, wd)

	err = internal.PgModelerExportToFile(
		filepath.Join(wd, fmt.Sprintf("%s.dbm", config.ModelName)),
		filepath.Join(wd, fmt.Sprintf("%s.sql", config.ModelName)),
	)
	if err != nil {
		log.Println(err)

		return "", ErrInvalidModel
	}

	err = internal.CreateUsers(migrateConn, config.DatabaseUsers)
	if err != nil {
		return "", fmt.Errorf("failed to setup migrate database: %w", err)
	}

	err = internal.CreateUsers(targetConn, config.DatabaseUsers)
	if err != nil {
		return "", fmt.Errorf("failed to setup target database: %w", err)
	}

	err = executeTargetSQL(targetConn, config)
	if err != nil {
		log.Println(err)

		return "", ErrInvalidModel
	}

	if initial {
		// If we are developing the schema initially, there will be no diffs,
		// and we want to copy over the schema file to the initial migration file
		var input []byte
		input, err = os.ReadFile(filepath.Join(wd, fmt.Sprintf("%s.sql", config.ModelName)))
		if err != nil {
			return "", fmt.Errorf("failed to read sql file: %w", err)
		}

		return string(input), nil
	}

	err = executeMigrateSQL(migrateConn)
	if err != nil {
		return "", fmt.Errorf("failed to execute migrate sql: %w", err)
	}

	statements, err := internal.Migra(internal.DSN(migrateConn, "disable"), internal.DSN(targetConn, "disable"))
	if err != nil {
		log.Println(err)

		return "", ErrInvalidModel
	}

	// Filter stuff from go-migrate that doesn't exist in the target db, and we don't have and need anyway
	statements = strings.ReplaceAll(
		statements,
		"alter table \"public\".\"schema_migrations\" drop constraint \"schema_migrations_pkey\";",
		"",
	)
	statements = strings.ReplaceAll(
		statements,
		"drop index if exists \"public\".\"schema_migrations_pkey\";",
		"",
	)
	statements = strings.ReplaceAll(
		statements,
		"drop table \"public\".\"schema_migrations\";",
		"",
	)
	statements = strings.Trim(statements, "\n")

	var lines []string
	for _, line := range strings.Split(statements, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	statements = strings.Join(lines, "\n") + "\n"

	return statements, nil
}

func executeMigrateSQL(migrateConn *pgx.Conn) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	m, err := migrate.New(fmt.Sprintf("file://%s", filepath.Join(wd, "migrations")), internal.DSN(migrateConn, "disable"))
	if err != nil {
		return fmt.Errorf("failed to create migrate: %w", err)
	}
	err = m.Up()
	if err != nil {
		return fmt.Errorf("failed to up migrations: %w", err)
	}

	return nil
}

func executeTargetSQL(targetConn *pgx.Conn, config *internal.Config) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	targetSQL, err := os.ReadFile(filepath.Join(wd, fmt.Sprintf("%s.sql", config.ModelName)))
	if err != nil {
		return fmt.Errorf("failed to read target sql: %w", err)
	}

	_, err = targetConn.Exec(context.Background(), string(targetSQL))
	if err != nil {
		return fmt.Errorf("failed to execute target sql: %w", err)
	}

	return nil
}

func inspectMigrations(migrationsDir string) (migrationsCount uint, err error) {
	err = filepath.WalkDir(migrationsDir, func(path string, d fs.DirEntry, err error) error {
		if path == migrationsDir {
			return nil
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		var regexpValidMigrationFilename = regexp.MustCompile(`^\d{3}_` + regexpPartialLowerKebabCase + `\.up\.sql$`)
		if !regexpValidMigrationFilename.MatchString(d.Name()) {
			//nolint:goerr113
			return fmt.Errorf("invalid existing migration filename %q", d.Name())
		}
		migrationsCount++

		return nil
	})

	//nolint:wrapcheck
	return migrationsCount, err
}
