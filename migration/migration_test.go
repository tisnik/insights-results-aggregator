/*
Copyright © 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package migration_test

import (
	"database/sql"
	sql_driver "database/sql/driver"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/stretchr/testify/assert"

	"github.com/RedHatInsights/insights-results-aggregator/tests/helpers"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RedHatInsights/insights-results-aggregator/migration"
)

const (
	dbClosedErrorMsg    = "sql: database is closed"
	noSuchTableErrorMsg = "no such table: migration_info"
	stepErrorMsg        = "migration Step Error"
)

var (
	stepNoopFn = func(tx *sql.Tx) error {
		return nil
	}
	stepErrorFn = func(tx *sql.Tx) error {
		return fmt.Errorf(stepErrorMsg)
	}
	stepRollbackFn = func(tx *sql.Tx) error {
		return tx.Rollback()
	}
	testMigration = migration.Migration{
		StepUp: func(tx *sql.Tx) error {
			_, err := tx.Exec("CREATE TABLE migration_test_table (col INTEGER)")
			return err
		},
		StepDown: func(tx *sql.Tx) error {
			_, err := tx.Exec("DROP TABLE migration_test_table")
			return err
		},
	}
)

func prepareDB(t *testing.T) *sql.DB {
	conn, err := sql.Open("sqlite3", ":memory:")
	helpers.FailOnError(t, err)

	return conn
}

func prepareDBAndInfo(t *testing.T) *sql.DB {
	db := prepareDB(t)

	if err := migration.InitInfoTable(db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}

	return db
}

func prepareDBAndMigrations(t *testing.T) *sql.DB {
	*migration.Migrations = []migration.Migration{testMigration}
	return prepareDBAndInfo(t)
}

// TestMigrationFull tests majority of the migration
// mechanism's functionality, all in one place.
func TestMigrationFull(t *testing.T) {
	// Don't overwrite the migration list, use the real migrations.
	db := prepareDBAndInfo(t)

	maxVer := migration.GetMaxVersion()
	assert.NotEqual(t, 0, maxVer, "no migrations available")

	currentVer, err := migration.GetDBVersion(db)
	helpers.FailOnError(t, err)

	assert.Equal(t, migration.Version(0), currentVer, "unexpected version")

	stepUpAndDown(t, db, maxVer, 0)
}

func stepUpAndDown(t *testing.T, db *sql.DB, upVer, downVer migration.Version) {
	err := migration.SetDBVersion(db, upVer)
	helpers.FailOnError(t, err)

	currentVer, err := migration.GetDBVersion(db)
	helpers.FailOnError(t, err)
	assert.Equal(t, upVer, currentVer, "unexpected version")

	err = migration.SetDBVersion(db, 0)
	helpers.FailOnError(t, err)

	currentVer, err = migration.GetDBVersion(db)
	helpers.FailOnError(t, err)
	assert.Equal(t, downVer, currentVer, "unexpected version")
}

// closeDB closes the connection to DB with check whether the close operation
// was successful or not.
func closeDB(t *testing.T, mockDB *sql.DB) {
	err := mockDB.Close()
	helpers.FailOnError(t, err)
}

// TestMigrationInit checks that database migration table initialization succeeds.
func TestMigrationInit(t *testing.T) {
	db := prepareDB(t)
	defer closeDB(t, db)

	err := migration.InitInfoTable(db)
	helpers.FailOnError(t, err)

	_, err = migration.GetDBVersion(db)
	helpers.FailOnError(t, err)
}

// TestMigrationReInit checks that an attempt to re-initialize an already initialized
// migration info table will simply result in a no-op without any error.
func TestMigrationReInit(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	err := migration.InitInfoTable(db)
	helpers.FailOnError(t, err)
}

func TestMigrationInitNotOneRow(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	_, err := db.Exec("INSERT INTO migration_info(version) VALUES(10)")
	helpers.FailOnError(t, err)

	const expectedErrStr = "unexpected number of rows in migration info table (expected: 1, reality: 2)"
	err = migration.InitInfoTable(db)
	assert.EqualError(t, err, expectedErrStr)
}

// TestMigrationGetVersion checks that the initial database migration version is 0.
func TestMigrationGetVersion(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	version, err := migration.GetDBVersion(db)
	helpers.FailOnError(t, err)

	assert.Equal(t, migration.Version(0), version, "unexpected database version")
}

func TestMigrationGetVersionMissingInfoTable(t *testing.T) {
	// Prepare DB without preparing the migration info table.
	db := prepareDB(t)
	defer closeDB(t, db)

	_, err := migration.GetDBVersion(db)
	assert.EqualError(t, err, noSuchTableErrorMsg)
}

func TestMigrationGetVersionMultipleRows(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	_, err := db.Exec("INSERT INTO migration_info(version) VALUES(10)")
	helpers.FailOnError(t, err)

	_, err = migration.GetDBVersion(db)
	assert.EqualError(t, err, "migration info table contain multiple rows")
}

func TestMigrationGetVersionEmptyTable(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	_, err := db.Exec("DELETE FROM migration_info")
	helpers.FailOnError(t, err)

	_, err = migration.GetDBVersion(db)
	assert.EqualError(t, err, "migration info table is empty")
}

func TestMigrationGetVersionInvalidType(t *testing.T) {
	db := prepareDB(t)
	defer closeDB(t, db)

	_, err := db.Exec("CREATE TABLE migration_info ( version TEXT )")
	helpers.FailOnError(t, err)

	_, err = db.Exec("INSERT INTO migration_info(version) VALUES('hello world')")
	helpers.FailOnError(t, err)

	const expectedErrStr = `sql: Scan error on column index 0, name "version": ` +
		`converting driver.Value type string ("hello world") to a uint: invalid syntax`
	_, err = migration.GetDBVersion(db)
	assert.EqualError(t, err, expectedErrStr)
}

// TestMigrationSetVersion checks that it is possible to change
// the database version in both direction (upgrade and downgrade).
func TestMigrationSetVersion(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	// Step-up from 0 to 1.
	err := migration.SetDBVersion(db, 1)
	helpers.FailOnError(t, err)

	version, err := migration.GetDBVersion(db)
	helpers.FailOnError(t, err)

	assert.Equal(t, migration.Version(1), version, "unexpected database version")

	// Step-down from 1 to 0.
	err = migration.SetDBVersion(db, 0)
	helpers.FailOnError(t, err)

	version, err = migration.GetDBVersion(db)
	helpers.FailOnError(t, err)

	assert.Equal(t, migration.Version(0), version, "unexpected database version")
}

func TestMigrationNoInfoTable(t *testing.T) {
	db := prepareDB(t)
	defer closeDB(t, db)

	// Intentionally missing info table initialization here.

	_, err := migration.GetDBVersion(db)
	assert.EqualError(
		t, err, noSuchTableErrorMsg, "migration info table should be missing when not initialized",
	)
}

func TestMigrationSetVersionSame(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	// Step-up from 0 to 1.
	err := migration.SetDBVersion(db, 1)
	helpers.FailOnError(t, err)

	// Set version to.
	err = migration.SetDBVersion(db, 1)
	helpers.FailOnError(t, err)

	version, err := migration.GetDBVersion(db)
	helpers.FailOnError(t, err)

	assert.Equal(t, migration.Version(1), version, "unexpected database version")
}

func TestMigrationSetVersionTargetTooHigh(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	// Step-up from 0 to 2 (impossible -- only 1 migration is available).
	err := migration.SetDBVersion(db, 2)
	assert.EqualError(t, err, "invalid target version (available version range is 0-1)")
}

// TestMigrationSetVersionUpError checks that an error during a step-up is correctly handled.
func TestMigrationSetVersionUpError(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	*migration.Migrations = []migration.Migration{
		{
			StepUp:   stepErrorFn,
			StepDown: stepNoopFn,
		},
	}

	err := migration.SetDBVersion(db, 1)
	assert.EqualError(t, err, stepErrorMsg)
}

// TestMigrationSetVersionDownError checks that an error during a step-down is correctly handled.
func TestMigrationSetVersionDownError(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	*migration.Migrations = []migration.Migration{
		{
			StepUp:   stepNoopFn,
			StepDown: stepErrorFn,
		},
	}

	// First we need to step-up before we can step-down.
	err := migration.SetDBVersion(db, 1)
	helpers.FailOnError(t, err)

	err = migration.SetDBVersion(db, 0)
	assert.EqualError(t, err, stepErrorMsg)
}

// TestMigrationSetVersionCurrentTooHighError makes sure that if the current DB version
// is outside of the available migration range, it is reported as an error.
func TestMigrationSetVersionCurrentTooHighError(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	_, err := db.Exec("UPDATE migration_info SET version=10")
	helpers.FailOnError(t, err)

	const expectedErrStr = "current version (10) is outside of available migration boundaries"
	err = migration.SetDBVersion(db, 0)
	assert.EqualError(t, err, expectedErrStr)
}

func TestMigrationInitClosedDB(t *testing.T) {
	db := prepareDB(t)
	// Intentionally no `defer` here.
	closeDB(t, db)

	err := migration.InitInfoTable(db)
	assert.EqualError(t, err, dbClosedErrorMsg)
}

func TestMigrationGetVersionClosedDB(t *testing.T) {
	db := prepareDBAndMigrations(t)
	// Intentionally no `defer` here.
	closeDB(t, db)

	_, err := migration.GetDBVersion(db)
	assert.EqualError(t, err, dbClosedErrorMsg)
}

func TestMigrationSetVersionClosedDB(t *testing.T) {
	db := prepareDBAndMigrations(t)
	// Intentionally no `defer` here.
	closeDB(t, db)

	err := migration.SetDBVersion(db, 0)
	assert.EqualError(t, err, dbClosedErrorMsg)
}

func TestMigrationInitRollbackStep(t *testing.T) {
	db := prepareDBAndMigrations(t)
	defer closeDB(t, db)

	*migration.Migrations = []migration.Migration{{
		StepUp:   stepRollbackFn,
		StepDown: stepNoopFn,
	}}

	const expectedErrStr = "sql: transaction has already been committed or rolled back"
	err := migration.SetDBVersion(db, 1)
	assert.EqualError(t, err, expectedErrStr)
}

func TestInitInfoTable_BeginTransactionDBError(t *testing.T) {
	db := prepareDB(t)
	closeDB(t, db)
	err := migration.InitInfoTable(db)
	assert.EqualError(t, err, "sql: database is closed")
}

func TestInitInfoTable_InitTableDBError(t *testing.T) {
	const errStr = "create table error"

	db, expects := helpers.MustGetMockDBWithExpects(t)
	defer helpers.MustGetMockDBWithExpects(t)

	expects.ExpectBegin()
	expects.ExpectExec("CREATE TABLE IF NOT EXISTS migration_info").WillReturnError(fmt.Errorf(errStr))
	expects.ExpectRollback()

	err := migration.InitInfoTable(db)
	assert.EqualError(t, err, errStr)
}

func TestInitInfoTable_InitVersionDBError(t *testing.T) {
	const errStr = "insert error"

	db, expects := helpers.MustGetMockDBWithExpects(t)
	defer helpers.MustGetMockDBWithExpects(t)

	expects.ExpectBegin()
	expects.ExpectExec("CREATE TABLE IF NOT EXISTS migration_info").WillReturnResult(sql_driver.ResultNoRows)
	expects.ExpectExec("INSERT INTO migration_info").WillReturnError(fmt.Errorf(errStr))
	expects.ExpectRollback()

	err := migration.InitInfoTable(db)
	assert.EqualError(t, err, errStr)
}

func TestInitInfoTable_CountDBError(t *testing.T) {
	const errStr = "count error"

	db, expects := helpers.MustGetMockDBWithExpects(t)
	defer helpers.MustGetMockDBWithExpects(t)

	expects.ExpectBegin()
	expects.ExpectExec("CREATE TABLE IF NOT EXISTS migration_info").WillReturnResult(sql_driver.ResultNoRows)
	expects.ExpectExec("INSERT INTO migration_info").WillReturnResult(sql_driver.ResultNoRows)
	expects.ExpectQuery("SELECT COUNT.+FROM migration_info").WillReturnError(fmt.Errorf(errStr))
	expects.ExpectRollback()

	err := migration.InitInfoTable(db)
	assert.EqualError(t, err, errStr)
}

func updateVersionInDBCommon(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	// set test migrations
	*migration.Migrations = []migration.Migration{testMigration}

	db, expects := helpers.MustGetMockDBWithExpects(t)

	expects.ExpectBegin()
	expects.ExpectExec("CREATE TABLE IF NOT EXISTS migration_info").WillReturnResult(sql_driver.ResultNoRows)
	expects.ExpectExec("INSERT INTO migration_info").WillReturnResult(sql_driver.ResultNoRows)
	expects.ExpectQuery("SELECT COUNT.+FROM migration_info").WillReturnRows(
		sqlmock.NewRows([]string{"version"}).AddRow(1),
	)
	expects.ExpectCommit()

	err := migration.InitInfoTable(db)
	helpers.FailOnError(t, err)

	expects.ExpectQuery("SELECT version FROM migration_info").WillReturnRows(
		sqlmock.NewRows([]string{"version"}).AddRow(0),
	)
	expects.ExpectBegin()
	expects.ExpectExec("CREATE TABLE migration_test_table").WillReturnResult(sql_driver.ResultNoRows)

	return db, expects
}

func TestUpdateVersionInDB_RowsAffectedError(t *testing.T) {
	const errStr = "rows affected error"

	db, expects := updateVersionInDBCommon(t)
	defer helpers.MustGetMockDBWithExpects(t)

	expects.ExpectExec("UPDATE migration_info SET version").
		WithArgs(1).
		WillReturnResult(sqlmock.NewErrorResult(fmt.Errorf(errStr)))

	err := migration.SetDBVersion(db, migration.GetMaxVersion())
	assert.EqualError(t, err, errStr)
}

func TestUpdateVersionInDB_MoreThan1RowAffected(t *testing.T) {
	db, expects := updateVersionInDBCommon(t)
	defer helpers.MustGetMockDBWithExpects(t)

	expects.ExpectExec("UPDATE migration_info SET version").
		WithArgs(1).
		WillReturnResult(sqlmock.NewResult(1, 2))

	// set test migrations
	*migration.Migrations = []migration.Migration{testMigration}

	err := migration.SetDBVersion(db, migration.GetMaxVersion())
	assert.EqualError(
		t, err, "unexpected number of affected rows in migration info table (expected: 1, reality: 2)",
	)
}

func TestWithTransaction_Panic(t *testing.T) {
	const errStr = "panic"

	db, expects := helpers.MustGetMockDBWithExpects(t)
	defer helpers.MustCloseMockDBWithExpects(t, db, expects)

	expects.ExpectBegin()
	expects.ExpectRollback()

	defer func() {
		p := recover()
		assert.Equal(t, p, errStr, "panic is expected")
	}()

	_ = migration.WithTransaction(db, func(tx *sql.Tx) error {
		panic(errStr)
	})
	t.Fatal("not expected to go here")
}
