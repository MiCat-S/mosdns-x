package control

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var mysqlUserTestColumns = []string{"id", "username", "role", "enabled", "expires_at_ns", "period", "timezone", "quota_limit", "qps", "burst", "max_credentials", "created_at_ns", "updated_at_ns", "password_salt", "password_hash", "password_version", "quota_start", "quota_high_start", "quota_used", "rate_tokens", "rate_at_ns"}
var mysqlCredentialTestColumns = []string{"id", "user_id", "name", "expires_at_ns", "revoked_at_ns", "created_at_ns", "updated_at_ns", "legacy_secret_hash", "token_hash", "version"}

func mysqlTestUserRow(now time.Time, used uint64, tokens float64, rateAt int64) *sqlmock.Rows {
	start := now.Truncate(24 * time.Hour).Unix()
	return sqlmock.NewRows(mysqlUserTestColumns).AddRow("user-1", "user", "user", true, nil, "daily", "UTC", 100, 10, 2, 3, now.Add(-time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano(), make([]byte, 16), make([]byte, 32), 1, start, start, used, tokens, rateAt)
}

func mysqlTestCredentialRow(now time.Time, tokenHash []byte) *sqlmock.Rows {
	return sqlmock.NewRows(mysqlCredentialTestColumns).AddRow("credential-1", "user-1", "phone", nil, nil, now.Add(-time.Hour).UnixNano(), now.Add(-time.Hour).UnixNano(), nil, tokenHash, 1)
}

func expectMySQLPublicListV5Upgrade(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(mysqlControlV5PublicListColumnsQuery)).WillReturnRows(sqlmock.NewRows([]string{"COLUMN_NAME"}))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlV5PublicListAlter(nil))).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_public_lists SET default_enabled=enabled, published=TRUE, snapshot_status=IF(last_refresh_status='success','current',IF(entry_count>0,'stale','missing')), snapshot_sha256='', last_successful_at_ns=IF(last_refresh_status='success',last_refreshed_at_ns,NULL)`)).WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestMySQLControlV5PublicListAlterIsIdempotent(t *testing.T) {
	existing := make(map[string]struct{}, len(mysqlControlV5PublicListColumns))
	for _, column := range mysqlControlV5PublicListColumns {
		existing[column.name] = struct{}{}
	}
	if got := mysqlControlV5PublicListAlter(existing); got != "" {
		t.Fatalf("complete schema alter=%q", got)
	}
	delete(existing, "published")
	got := mysqlControlV5PublicListAlter(existing)
	if !strings.Contains(got, "ADD COLUMN published BOOLEAN NOT NULL DEFAULT TRUE") || strings.Count(got, "ADD COLUMN") != 1 {
		t.Fatalf("partial schema alter=%q", got)
	}
}

func TestMySQLAuthenticateCredential(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	store := &MySQLStore{db: db, clock: clock, operationTimeout: time.Second, closeCh: make(chan struct{})}
	token := "7a2167b1-8018-4e08-8c3e-8099ef232861"
	hash := hashSecret(token)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlCredentialColumns + ` FROM mosdns_credentials WHERE token_hash=?`)).WithArgs(hash[:]).WillReturnRows(mysqlTestCredentialRow(now, hash[:]))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ?`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 0, 12, now.UnixNano()))

	identity, err := store.AuthenticateCredential(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if identity != (Identity{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1}) {
		t.Fatalf("identity=%+v", identity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMySQLControlUpgradesV1Schema(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(legacyMySQLControlSchemaVersion))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLPublicListV5Upgrade(mock)
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_schema_migrations SET version=? WHERE component='control'`)).WithArgs(mysqlControlSchemaVersion).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMySQLControlUpgradesV2PolicySettings(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(policyMySQLControlSchemaVersion))
	mock.ExpectQuery(regexp.QuoteMeta(mysqlControlV3ColumnsQuery)).WillReturnRows(sqlmock.NewRows([]string{"COLUMN_NAME"}))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlV3Alter(nil))).WillReturnResult(sqlmock.NewResult(0, 0))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLPublicListV5Upgrade(mock)
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_schema_migrations SET version=? WHERE component='control'`)).WithArgs(mysqlControlSchemaVersion).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMySQLControlUpgradesV3PublicLists(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(policySwitchMySQLSchemaVersion))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLPublicListV5Upgrade(mock)
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_schema_migrations SET version=? WHERE component='control'`)).WithArgs(mysqlControlSchemaVersion).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMySQLControlResumesV3UpgradeAfterAlter(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(policyMySQLControlSchemaVersion))
	columns := sqlmock.NewRows([]string{"COLUMN_NAME"})
	for _, column := range mysqlControlV3Columns {
		columns.AddRow(column.name)
	}
	mock.ExpectQuery(regexp.QuoteMeta(mysqlControlV3ColumnsQuery)).WillReturnRows(columns)
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLPublicListV5Upgrade(mock)
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_schema_migrations SET version=? WHERE component='control'`)).WithArgs(mysqlControlSchemaVersion).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeMySQLControlConvergesSchemaWhenVersionRowIsMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLPublicListV5Upgrade(mock)
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_schema_migrations (component, version) VALUES ('control', ?)`)).WithArgs(mysqlControlSchemaVersion).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLAdmitUpdatesQuotaRateAndUsageAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 11, 12, 34, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	store := &MySQLStore{db: db, clock: clock, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlUserColumns + ` FROM mosdns_users WHERE id = ? FOR UPDATE`)).WithArgs("user-1").WillReturnRows(mysqlTestUserRow(now, 2, 5, now.Add(-500*time.Millisecond).UnixNano()))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT ` + mysqlCredentialColumns + ` FROM mosdns_credentials WHERE id=? FOR UPDATE`)).WithArgs("credential-1").WillReturnRows(mysqlTestCredentialRow(now, make([]byte, 32)))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE mosdns_users SET`)).WillReturnResult(sqlmock.NewResult(0, 1))
	for range 3 {
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO mosdns_usage_minutes`)).WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()

	err = store.Admit(context.Background(), Identity{UserID: "user-1", CredentialID: "credential-1", CredentialVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLAuthenticateSessionAfterCloseIsUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	store := &MySQLStore{db: db, clock: realClock{}, operationTimeout: time.Second, closeCh: make(chan struct{})}
	mock.ExpectClose()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AuthenticateSession(context.Background(), "id.secret"); err != ErrUnavailable {
		t.Fatalf("error=%v", err)
	}
}

func expectMySQLAdditivePolicyColumnsAdded(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(mysqlAdditivePolicyColumnsQuery())).WillReturnRows(sqlmock.NewRows([]string{"COLUMN_NAME"}))
	mock.ExpectExec(regexp.QuoteMeta(mysqlAdditivePolicyAlter(nil))).WillReturnResult(sqlmock.NewResult(0, 0))
}

// A database already at the current version gains answer_family and nothing
// else. The v5 data migration must not rerun, since it resets what
// administrators published, and the version row must not change, so an older
// binary can still open the database after a rollback. sqlmock rejects any
// statement not expected here, including the v5 UPDATE and a version write.
func TestInitializeMySQLControlAddsAnswerFamilyWithoutVersionBump(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(mysqlControlSchemaVersion))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectMySQLAdditivePolicyColumnsAdded(mock)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// When answer_family already exists the step issues no ALTER, which would
// fail on a duplicate column. This is the steady state of every later start.
func TestInitializeMySQLControlSkipsExistingAnswerFamilyColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT GET_LOCK('mosdns_x_control_schema', 10)`)).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(mysqlControlMigrations[0])).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM mosdns_schema_migrations WHERE component = 'control'`)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(mysqlControlSchemaVersion))
	for _, statement := range mysqlControlMigrations[1:] {
		mock.ExpectExec(regexp.QuoteMeta(statement)).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	existing := sqlmock.NewRows([]string{"COLUMN_NAME"})
	for _, column := range mysqlAdditivePolicyColumns {
		existing.AddRow(column.name)
	}
	mock.ExpectQuery(regexp.QuoteMeta(mysqlAdditivePolicyColumnsQuery())).WillReturnRows(existing)
	mock.ExpectExec(regexp.QuoteMeta(`SELECT RELEASE_LOCK('mosdns_x_control_schema')`)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := initializeMySQLControl(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A table that received only some columns, from an earlier release or an
// interrupted start, gets exactly the missing ones and nothing twice.
func TestMySQLAdditivePolicyAlterAddsOnlyMissingColumns(t *testing.T) {
	if got := mysqlAdditivePolicyAlter(map[string]struct{}{}); !strings.Contains(got, "ADD COLUMN answer_family") || !strings.Contains(got, "ADD COLUMN shuffle_answers") {
		t.Fatalf("empty table alter = %q", got)
	}
	existing := map[string]struct{}{"answer_family": {}, "ttl_min": {}}
	got := mysqlAdditivePolicyAlter(existing)
	for _, name := range []string{"answer_family", "ttl_min"} {
		if strings.Contains(got, "ADD COLUMN "+name+" ") {
			t.Fatalf("existing column %s added again: %q", name, got)
		}
	}
	for _, name := range []string{"ttl_max", "flatten_cname", "shuffle_answers"} {
		if !strings.Contains(got, "ADD COLUMN "+name+" ") {
			t.Fatalf("missing column %s not added: %q", name, got)
		}
	}
	all := map[string]struct{}{}
	for _, column := range mysqlAdditivePolicyColumns {
		all[column.name] = struct{}{}
	}
	if got := mysqlAdditivePolicyAlter(all); got != "" {
		t.Fatalf("complete table alter = %q, want none", got)
	}
}
