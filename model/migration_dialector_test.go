package model

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MigrationIdentityFields struct {
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"size:64;unique"`
	Reference string `gorm:"size:64;uniqueIndex"`
	Provider  string `gorm:"size:32;uniqueIndex:,composite:provider_subject"`
	Subject   string `gorm:"size:64;uniqueIndex:,composite:provider_subject"`
}

type migrationIdentityV1 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(32)"`
}

type migrationIdentityV2 struct {
	MigrationIdentityFields
	Digest string `gorm:"type:char(64)"`
	Note   string `gorm:"size:128"`
}

type migrationConstraintV1 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64"`
}

type migrationConstraintV2 struct {
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"size:64;unique"`
}

type migrationDecimalV1 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(10,6);default:0"`
}

type migrationDecimalV2 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:0"`
}

type migrationDecimalV3 struct {
	ID    int     `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(12,6);not null;default:1.25"`
}

type paymentGatewayReleasedTopUp struct {
	Id              int     `json:"id"`
	UserId          int     `json:"user_id" gorm:"index"`
	Amount          int64   `json:"amount"`
	Money           float64 `json:"money"`
	TradeNo         string  `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string  `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string  `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	CreateTime      int64   `json:"create_time"`
	CompleteTime    int64   `json:"complete_time"`
	Status          string  `json:"status"`
}

func (paymentGatewayReleasedTopUp) TableName() string { return "top_ups" }

type paymentGatewayReleasedSubscriptionOrder struct {
	Id     int     `json:"id"`
	UserId int     `json:"user_id" gorm:"index"`
	PlanId int     `json:"plan_id" gorm:"index"`
	Money  float64 `json:"money"`

	TradeNo         string `json:"trade_no" gorm:"unique;type:varchar(255);index"`
	PaymentMethod   string `json:"payment_method" gorm:"type:varchar(50)"`
	PaymentProvider string `json:"payment_provider" gorm:"type:varchar(50);default:''"`
	Status          string `json:"status"`
	CreateTime      int64  `json:"create_time"`
	CompleteTime    int64  `json:"complete_time"`

	ProviderPayload string `json:"provider_payload" gorm:"type:text"`
}

func (paymentGatewayReleasedSubscriptionOrder) TableName() string { return "subscription_orders" }

func TestPaymentGatewayOfficialDatabaseMigration(t *testing.T) {
	sourcePath := os.Getenv("PAYMENT_GATEWAY_OFFICIAL_DB")
	if sourcePath == "" {
		t.Skip("PAYMENT_GATEWAY_OFFICIAL_DB is not configured")
	}
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	originalHash := sha256.Sum256(source)
	originalDB, originalLogDB := DB, LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalPath := common.SQLitePath
	originalDeadline := legacyAccessTokenRetireAt.Load()
	common.SQLitePath = filepath.Join(t.TempDir(), "official-copy.db")
	require.NoError(t, os.WriteFile(common.SQLitePath, source, 0600))
	t.Setenv("PAYMENT_GATEWAY_TEST_DSN", "local")
	db, databaseType, err := chooseDB("PAYMENT_GATEWAY_TEST_DSN", false)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		DB, LOG_DB, common.SQLitePath = originalDB, originalLogDB, originalPath
		common.SetDatabaseTypes(originalMainType, originalLogType)
		initCol()
		legacyAccessTokenRetireAt.Store(originalDeadline)
	})
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(databaseType, databaseType)
	initCol()
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		counts[table] = count
	}
	var originalOptions []Option
	require.NoError(t, db.Select(commonKeyCol, "value").Order(commonKeyCol).Find(&originalOptions).Error)
	optionKeys := make([]string, 0, len(originalOptions))
	for _, option := range originalOptions {
		optionKeys = append(optionKeys, option.Key)
	}
	originalOptionsJSON, err := common.Marshal(originalOptions)
	require.NoError(t, err)
	paymentSnapshots := make(map[string]struct {
		columns []string
		hash    [32]byte
	})
	for _, table := range []string{"top_ups", "subscription_orders"} {
		columns, err := db.Table(table).Migrator().ColumnTypes(table)
		require.NoError(t, err)
		projection := make([]string, 0, len(columns))
		for _, column := range columns {
			projection = append(projection, column.Name())
		}
		var rows []map[string]any
		require.NoError(t, db.Table(table).Select(projection).Order("id").Find(&rows).Error)
		encoded, err := common.Marshal(rows)
		require.NoError(t, err)
		paymentSnapshots[table] = struct {
			columns []string
			hash    [32]byte
		}{projection, sha256.Sum256(encoded)}
	}
	var originalTopups []TopUp
	var originalOrders []SubscriptionOrder
	var originalWallets []struct {
		Id        int
		Quota     int
		UsedQuota int
	}
	require.NoError(t, db.Order("id").Find(&originalTopups).Error)
	require.NoError(t, db.Order("id").Find(&originalOrders).Error)
	require.NoError(t, db.Table("users").Select("id, quota, used_quota").Order("id").Find(&originalWallets).Error)
	originalTopupsJSON, err := common.Marshal(originalTopups)
	require.NoError(t, err)
	originalOrdersJSON, err := common.Marshal(originalOrders)
	require.NoError(t, err)
	originalWalletsJSON, err := common.Marshal(originalWallets)
	require.NoError(t, err)
	require.NoError(t, migrateDB())
	require.NoError(t, migrateDB())
	for table, expected := range counts {
		if table == "options" && !slices.Contains(optionKeys, legacyAccessTokenRetireAtKey) {
			expected++
		}
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		assert.Equal(t, expected, count, table)
	}
	var preservedOptions []Option
	require.NoError(t, db.Select(commonKeyCol, "value").Where(commonKeyCol+" IN ?", optionKeys).Order(commonKeyCol).Find(&preservedOptions).Error)
	preservedOptionsJSON, err := common.Marshal(preservedOptions)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(originalOptionsJSON), sha256.Sum256(preservedOptionsJSON), "existing settings changed")
	for table, snapshot := range paymentSnapshots {
		var rows []map[string]any
		require.NoError(t, db.Table(table).Select(snapshot.columns).Order("id").Find(&rows).Error)
		encoded, err := common.Marshal(rows)
		require.NoError(t, err)
		assert.Equal(t, snapshot.hash, sha256.Sum256(encoded), "stored payment fields changed: %s", table)
	}
	var topups []TopUp
	var orders []SubscriptionOrder
	var wallets []struct {
		Id        int
		Quota     int
		UsedQuota int
	}
	require.NoError(t, db.Order("id").Find(&topups).Error)
	require.NoError(t, db.Order("id").Find(&orders).Error)
	require.NoError(t, db.Table("users").Select("id, quota, used_quota").Order("id").Find(&wallets).Error)
	topupsJSON, err := common.Marshal(topups)
	require.NoError(t, err)
	ordersJSON, err := common.Marshal(orders)
	require.NoError(t, err)
	walletsJSON, err := common.Marshal(wallets)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(originalTopupsJSON), sha256.Sum256(topupsJSON), "top-up records changed")
	assert.Equal(t, sha256.Sum256(originalOrdersJSON), sha256.Sum256(ordersJSON), "subscription orders changed")
	assert.Equal(t, sha256.Sum256(originalWalletsJSON), sha256.Sum256(walletsJSON), "wallet balances changed")
	sourceAfter, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	assert.Equal(t, originalHash, sha256.Sum256(sourceAfter), "source database was modified")
	t.Logf("copied official.db: %d users, %d top-ups and %d subscription orders preserved after two migrations", len(wallets), len(topups), len(orders))
}

func TestPaymentGatewayDatabaseMatrix(t *testing.T) {
	originalDB, originalLogDB := DB, LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalDeadline := legacyAccessTokenRetireAt.Load()
	t.Cleanup(func() {
		DB, LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
		initCol()
		legacyAccessTokenRetireAt.Store(originalDeadline)
	})
	for _, testCase := range []struct {
		name         string
		env          string
		databaseType common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "PAYMENT_GATEWAY_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "PAYMENT_GATEWAY_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, upgrade := range []bool{false, true} {
				t.Run(fmt.Sprintf("upgrade=%t", upgrade), func(t *testing.T) {
					dsn := os.Getenv(testCase.env)
					if testCase.name == "sqlite" {
						originalPath := common.SQLitePath
						common.SQLitePath = filepath.Join(t.TempDir(), "payments.db")
						t.Cleanup(func() { common.SQLitePath = originalPath })
						dsn = "local"
					} else if dsn == "" {
						t.Skip(testCase.env + " is not configured")
					}
					t.Setenv("PAYMENT_GATEWAY_TEST_DSN", dsn)
					db, databaseType, err := chooseDB("PAYMENT_GATEWAY_TEST_DSN", false)
					require.NoError(t, err)
					require.Equal(t, testCase.databaseType, databaseType)
					sqlDB, err := db.DB()
					require.NoError(t, err)
					t.Cleanup(func() { _ = sqlDB.Close() })
					sqlDB.SetMaxOpenConns(1)
					versionQuery := "SELECT sqlite_version()"
					if testCase.name != "sqlite" {
						nameQuery := "SELECT DATABASE()"
						if testCase.name == "postgres" {
							nameQuery = "SELECT current_database()"
						}
						var name string
						require.NoError(t, db.Raw(nameQuery).Scan(&name).Error)
						require.Equal(t, "newapi_payment_verify", name, "matrix must use an isolated verification database")
						versionQuery = "SELECT VERSION()"
					}
					var version string
					require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
					t.Logf("database version: %s; upgrade baseline: v1.0.0-rc.47", version)
					DB, LOG_DB = db, db
					common.SetDatabaseTypes(databaseType, databaseType)
					initCol()
					tables, err := db.Migrator().GetTables()
					require.NoError(t, err)
					for _, table := range tables {
						require.NoError(t, db.Migrator().DropTable(table))
					}
					if upgrade {
						require.NoError(t, db.AutoMigrate(&paymentGatewayReleasedTopUp{}, &paymentGatewayReleasedSubscriptionOrder{}))
						require.NoError(t, db.Create(&paymentGatewayReleasedTopUp{Id: 1, UserId: 1, Amount: 2, Money: 2.34, TradeNo: "released-topup", PaymentProvider: PaymentProviderEpay, PaymentMethod: "alipay", Status: common.TopUpStatusSuccess}).Error)
						require.NoError(t, db.Create(&paymentGatewayReleasedSubscriptionOrder{Id: 1, UserId: 1, PlanId: 1, Money: 9.99, TradeNo: "released-subscription", PaymentProvider: PaymentProviderStripe, PaymentMethod: PaymentMethodStripe, Status: common.TopUpStatusPending, ProviderPayload: "released-payload"}).Error)
					}
					require.NoError(t, migrateDB())
					recorder := &migrationSQLRecorder{}
					DB = db.Session(&gorm.Session{Logger: recorder})
					require.NoError(t, migrateDB())
					for _, statement := range recorder.schemaMutations() {
						if strings.Contains(statement, "top_ups") || strings.Contains(statement, "subscription_orders") {
							assert.Fail(t, "payment schema changed on repeated migration", statement)
						}
					}
					if upgrade {
						var topup TopUp
						require.NoError(t, db.First(&topup, 1).Error)
						assert.Equal(t, "released-topup", topup.TradeNo)
						assert.EqualValues(t, 2, topup.Amount)
						assert.Equal(t, 2.34, topup.Money)
						assert.Equal(t, common.TopUpStatusSuccess, topup.Status)
						assert.Equal(t, PaymentProviderEpay, topup.PaymentProvider)
						var subscription SubscriptionOrder
						require.NoError(t, db.First(&subscription, 1).Error)
						assert.Equal(t, "released-subscription", subscription.TradeNo)
						assert.Equal(t, 9.99, subscription.Money)
						assert.Equal(t, "released-payload", subscription.ProviderPayload)
						assert.Error(t, db.Create(&TopUp{TradeNo: topup.TradeNo}).Error)
						assert.Error(t, db.Create(&SubscriptionOrder{TradeNo: subscription.TradeNo}).Error)
						require.NoError(t, db.Delete(&topup).Error)
						require.NoError(t, db.Delete(&subscription).Error)
					}
					DB = db
					t.Run("topup settlement", TestPaymentGatewayTopUpSettlement)
					t.Run("unsafe credit", TestPaymentGatewayTopUpRejectsUnsafeCredit)
					t.Run("subscription settlement", TestPaymentGatewaySubscriptionCompletion)
				})
			}
		})
	}
}

func TestMigrationSchemaStability(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dsn string
			switch dialect {
			case "sqlite":
				dsn = "local"
				previousPath := common.SQLitePath
				common.SQLitePath = filepath.Join(t.TempDir(), "migration.db")
				t.Cleanup(func() { common.SQLitePath = previousPath })
			case "mysql":
				dsn = os.Getenv("TEST_MYSQL_DSN")
			case "postgres":
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("MIGRATION_TEST_DSN", dsn)
			db, _, err := chooseDB("MIGRATION_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			recorder := &migrationSQLRecorder{}
			db = db.Session(&gorm.Session{Logger: recorder})

			t.Run("identity_and_indexes", func(t *testing.T) {
				const table = "migration_identity_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				row := migrationIdentityV1{
					MigrationIdentityFields: MigrationIdentityFields{ID: 1, Name: "root", Reference: "token-reference", Provider: "oidc", Subject: "subject"},
					Digest:                  "old-digest",
				}
				require.NoError(t, db.Table(table).Create(&row).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV1{}))
				assert.Empty(t, recorder.schemaMutations())

				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				columns, err := db.Table(table).Migrator().ColumnTypes(&migrationIdentityV2{})
				require.NoError(t, err)
				for _, column := range columns {
					if column.Name() == "digest" {
						length, ok := column.Length()
						require.True(t, ok)
						assert.EqualValues(t, 64, length)
					}
				}
				assert.True(t, db.Table(table).Migrator().HasColumn(&migrationIdentityV2{}, "note"))
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationIdentityV2{}))
				assert.Empty(t, recorder.schemaMutations())
				var saved migrationIdentityV2
				require.NoError(t, db.Table(table).First(&saved, 1).Error)
				assert.Equal(t, row.MigrationIdentityFields, saved.MigrationIdentityFields)
				expectedDigest := row.Digest
				if dialect == "postgres" {
					expectedDigest += strings.Repeat(" ", 64-len(row.Digest))
				}
				assert.Equal(t, expectedDigest, saved.Digest)
				for _, duplicate := range []migrationIdentityV2{
					{MigrationIdentityFields: MigrationIdentityFields{Name: "root", Reference: "other-1", Provider: "other", Subject: "1"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-2", Reference: "token-reference", Provider: "other", Subject: "2"}},
					{MigrationIdentityFields: MigrationIdentityFields{Name: "other-3", Reference: "other-3", Provider: "oidc", Subject: "subject"}},
				} {
					assert.Error(t, db.Table(table).Create(&duplicate).Error)
				}
			})

			t.Run("unique_constraint_changes", func(t *testing.T) {
				const table = "migration_constraint_test"
				t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Error(t, db.Table(table).Create(&migrationConstraintV2{Name: "existing"}).Error)
				recorder.reset()
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV2{}))
				assert.Empty(t, recorder.schemaMutations())
				require.NoError(t, db.Table(table).AutoMigrate(&migrationConstraintV1{}))
				require.NoError(t, db.Table(table).Create(&migrationConstraintV1{Name: "existing"}).Error)
			})

			if dialect == "postgres" {
				t.Run("renamed_unique_constraints", func(t *testing.T) {
					const table = "migration_renamed_unique"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					tableDB := db.Table(table).Session(&gorm.Session{})
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					original := migrationConstraintV1{Name: "existing"}
					require.NoError(t, tableDB.Create(&original).Error)
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (name)", clause.Table{Name: table}, clause.Column{Name: name}).Error)
					}
					require.NoError(t, db.Exec("ALTER TABLE ? ADD CONSTRAINT keep_composite UNIQUE (id, name)", clause.Table{Name: table}).Error)
					require.NoError(t, db.Exec("CREATE INDEX keep_name_lookup ON ? (name)", clause.Table{Name: table}).Error)
					// A unique field must keep the old names and still reject duplicates.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV2{}))
					require.Error(t, tableDB.Create(&migrationConstraintV2{Name: "existing"}).Error)
					require.NoError(t, db.Exec("CREATE TABLE migration_unique_reference (name varchar(64) REFERENCES migration_renamed_unique(name))").Error)
					t.Cleanup(func() { _ = db.Migrator().DropTable("migration_unique_reference") })
					require.NoError(t, db.Exec("INSERT INTO migration_unique_reference (name) VALUES (?)", "existing").Error)
					require.Error(t, tableDB.AutoMigrate(&migrationConstraintV1{}), "dependent foreign keys must not be cascaded away")
					for _, name := range []string{"models_model_name_key", `imported "model" name`} {
						assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, name), "all old constraints must survive rollback")
					}
					var references int64
					require.NoError(t, db.Table("migration_unique_reference").Count(&references).Error)
					assert.EqualValues(t, 1, references)
					require.NoError(t, db.Migrator().DropTable("migration_unique_reference"))
					// Removing column uniqueness must resolve both actual constraint names.
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					recorder.reset()
					require.NoError(t, tableDB.AutoMigrate(&migrationConstraintV1{}))
					assert.Empty(t, recorder.schemaMutations())
					require.NoError(t, tableDB.Create(&migrationConstraintV1{Name: "existing"}).Error)
					assert.True(t, tableDB.Migrator().HasConstraint(&migrationConstraintV1{}, "keep_composite"))
					assert.True(t, tableDB.Migrator().HasIndex(&migrationConstraintV1{}, "keep_name_lookup"))
					var rows []migrationConstraintV1
					require.NoError(t, tableDB.Order("id").Find(&rows).Error)
					require.Len(t, rows, 2)
					assert.Equal(t, original, rows[0])
				})
			}

			if dialect == "mysql" {
				t.Run("decimal_default_and_real_changes", func(t *testing.T) {
					const table = "migration_decimal_test"
					t.Cleanup(func() { _ = db.Migrator().DropTable(table) })
					require.NoError(t, db.Table(table).AutoMigrate(&migrationDecimalV1{}))
					require.NoError(t, db.Table(table).Create(&migrationDecimalV1{ID: 1, Price: 12.345678}).Error)
					for _, target := range []any{&migrationDecimalV1{}, &migrationDecimalV2{}, &migrationDecimalV3{}} {
						require.NoError(t, db.Table(table).AutoMigrate(target))
						recorder.reset()
						require.NoError(t, db.Table(table).AutoMigrate(target))
						assert.Empty(t, recorder.schemaMutations())
					}
					columns, err := db.Table(table).Migrator().ColumnTypes(&migrationDecimalV3{})
					require.NoError(t, err)
					for _, column := range columns {
						if column.Name() == "price" {
							precision, scale, ok := column.DecimalSize()
							require.True(t, ok)
							assert.EqualValues(t, 12, precision)
							assert.EqualValues(t, 6, scale)
							nullable, ok := column.Nullable()
							require.True(t, ok)
							assert.False(t, nullable)
						}
					}
					require.NoError(t, db.Table(table).Create(&map[string]any{"id": 2}).Error)
					var prices []float64
					require.NoError(t, db.Table(table).Order("id").Pluck("price", &prices).Error)
					assert.Equal(t, []float64{12.345678, 1.25}, prices)
				})
			}
		})
	}
}
