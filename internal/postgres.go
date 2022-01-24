package internal

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
)

const (
	//nolint:gochecknoglobals
	PGDefaultUsername = "postgres"
	//nolint:gochecknoglobals
	PGDefaultPassword = "postgres"
	//nolint:gochecknoglobals
	PGDefaultDatabase = "postgres"
)

func getEnv(password, sslmode string) []string {
	return append(
		os.Environ(),
		fmt.Sprintf("PGPASSWORD=%s", password),
		fmt.Sprintf("PGSSLMODE=%s", sslmode),
	)
}

func PgIsReady(ip, user, password, sslmode string) (up bool, out []byte) {
	cmdPgIsReady := exec.Command(
		"pg_isready",
		"--user",
		user,
		"--host",
		ip,
		"--dbname",
		PGDefaultDatabase,
	)
	cmdPgIsReady.Env = getEnv(password, sslmode)
	out, err := cmdPgIsReady.CombinedOutput()

	return err == nil, out
}

func PsqlWaitDatabaseUp(ip, user, password, sslmode string) {
	var connected bool
	var out []byte
	count := 0
	for {
		if count == 10 {
			log.Fatalf("Failed to connect to database: %s\n", string(out))
		}
		if connected, out = PgIsReady(ip, user, password, sslmode); connected {
			break
		} else {
			count++
			time.Sleep(time.Second)
		}
	}
}

func PsqlCommand(ip, user, password, sslmode, database, command string) error {
	cmdPsql := exec.Command(
		"psql",
		"--echo-errors",
		"--variable",
		"ON_ERROR_STOP=1",
		"--user",
		user,
		"--host",
		ip,
		"--command",
		command,
		database,
	)
	cmdPsql.Env = getEnv(password, sslmode)
	cmdPsql.Stderr = os.Stderr

	out, err := cmdPsql.Output()
	if err != nil {
		return fmt.Errorf("failed to run psql: %w %v", err, string(out))
	}

	return nil
}

func PsqlFile(ip, user, password, sslmode, database, file string) error {
	cmdPsql := exec.Command(
		"psql",
		"--echo-errors",
		"--variable",
		"ON_ERROR_STOP=1",
		"--user",
		user,
		"--host",
		ip,
		"--file",
		file,
		database,
	)
	cmdPsql.Env = getEnv(password, sslmode)
	cmdPsql.Stderr = os.Stderr

	out, err := cmdPsql.Output()
	if err != nil {
		return fmt.Errorf("failed to run psql: %w %v", err, string(out))
	}

	return nil
}

func PgDump(ip, user, password, sslmode, database string, args []string) (string, error) {
	cmd := []string{
		"pg_dump",
		"--user",
		user,
		"--host",
		ip,
	}
	cmd = append(cmd, args...)
	cmd = append(cmd, database)

	//nolint:gosec
	cmdPgDump := exec.Command(cmd[0], cmd[1:]...)
	cmdPgDump.Env = getEnv(password, sslmode)
	cmdPgDump.Stderr = os.Stderr

	stdout, err := cmdPgDump.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run pg_dump: %w", err)
	}

	return string(stdout), nil
}

func PsqlHelperSetupDatabaseAndUsers(ip, user, password, sslmode, database string, users []string) error {
	err := PsqlCommand(ip, user, password, sslmode, PGDefaultDatabase, fmt.Sprintf("CREATE DATABASE %q;", database))
	if err != nil {
		return err
	}
	for _, u := range users {
		err = PsqlCommand(ip, user, password, sslmode, PGDefaultDatabase, fmt.Sprintf("CREATE ROLE %q WITH LOGIN;", u))
		if err != nil {
			return err
		}
	}

	return nil
}

func PsqlHelperSetupDatabaseAndUsersDrop(ip, user, password, sslmode, database string, users []string) error {
	err := PsqlCommand(
		ip,
		user,
		password,
		sslmode,
		PGDefaultDatabase,
		fmt.Sprintf("DROP DATABASE IF EXISTS %q WITH (FORCE)", database),
	)
	if err != nil {
		return err
	}
	for _, u := range users {
		err = PsqlCommand(ip, user, password, sslmode, PGDefaultDatabase, fmt.Sprintf("DROP ROLE IF EXISTS %s", u))
		if err != nil {
			return err
		}
	}

	return nil
}
