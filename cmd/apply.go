package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// needed driver.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/spf13/cobra"

	"github.com/stack11/trek/internal"
)

//nolint:gochecknoglobals
var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the migrations to a running database",
	Run: func(cmd *cobra.Command, args []string) {
		internal.AssertApplyToolsAvailable()

		config, err := internal.ReadConfig()
		if err != nil {
			log.Fatalf("Failed to read config: %v\n", err)
		}

		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get working directory: %v\n", err)
		}

		pgHost := os.Getenv("TREK_PGHOST")
		pgPort := os.Getenv("TREK_PGPORT")
		pgUser := os.Getenv("TREK_PGUSER")
		pgPassword := os.Getenv("TREK_PGPASSWORD")
		resetDB := os.Getenv("TREK_RESET_DB") == "true"
		insertTestData := os.Getenv("TREK_INSERT_TEST_DATA") == "true"
		sslMode := internal.GetSSLMode()
		migrateDSN := fmt.Sprintf(
			"postgres://%s:%s@%s:%s/%s?sslmode=%s",
			pgUser,
			pgPassword,
			pgHost,
			pgPort,
			config.DatabaseName,
			sslMode,
		)

		internal.PsqlWaitDatabaseUp(pgHost, pgPort, pgUser, pgPassword, sslMode)

		if resetDB {
			// Pass empty user list so the roles don't get dropped
			err = internal.PsqlHelperSetupDatabaseAndUsersDrop(
				pgHost,
				pgPort,
				pgUser,
				pgPassword,
				sslMode,
				config.DatabaseName,
				[]string{},
			)
			if err != nil {
				log.Println(err)
			}
		}

		// It will fail on roles that already exist, but that can be ignored
		err = internal.PsqlHelperSetupDatabaseAndUsers(
			pgHost,
			pgPort,
			pgUser,
			pgPassword,
			sslMode,
			config.DatabaseName,
			config.DatabaseUsers,
		)
		if err != nil {
			log.Println(err)
		}

		m, err := migrate.New(fmt.Sprintf("file://%s", filepath.Join(wd, "migrations")), migrateDSN)
		if err != nil {
			log.Fatalln(err)
		}

		if resetDB {
			var files []os.DirEntry
			files, err = os.ReadDir(filepath.Join(wd, "migrations"))
			if err != nil {
				log.Fatalln(err)
			}

			for index, file := range files {
				log.Printf("Running migration %s\n", file.Name())
				err = m.Steps(1)
				if errors.Is(err, migrate.ErrNoChange) {
					log.Println("No changes!")
				} else if err != nil {
					log.Fatalln(err)
				}
				if insertTestData {
					err = filepath.Walk(filepath.Join(wd, "testdata"), func(p string, info fs.FileInfo, err error) error {
						if strings.HasPrefix(path.Base(p), fmt.Sprintf("%03d", index+1)) {
							log.Printf("Inserting test data %s\n", path.Base(p))

							err := internal.PsqlFile(pgHost, pgUser, pgPassword, sslMode, config.DatabaseName, p)
							if err != nil {
								return fmt.Errorf("failed to apply test data: %w", err)
							}

							return nil
						}

						return nil
					})
					if err != nil {
						log.Fatalf("Failed to run testdata: %v\n", err)
					}
				}
			}
		} else {
			err = m.Up()
			if errors.Is(err, migrate.ErrNoChange) {
				log.Println("No changes!")
			} else if err != nil {
				log.Fatalln(err)
			}
		}
		log.Println("Successfully migrated database")
	},
}
