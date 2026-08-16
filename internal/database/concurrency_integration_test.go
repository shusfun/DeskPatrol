package database

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DESKPATROL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置隔离测试数据库 DESKPATROL_TEST_DATABASE_URL")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE administrators CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestActivationCodeConcurrentRedeemAllowsOneInstallation(t *testing.T) {
	pool := testPool(t)
	var administratorID int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO administrators(login_name,password_hash) VALUES('admin','hash') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatal(err)
	}
	codeID := "10000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(context.Background(), `INSERT INTO activation_codes(id,code_hash,expires_at,created_by) VALUES($1,'digest',NOW()+INTERVAL '1 day',$2)`, codeID, administratorID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wait sync.WaitGroup
	for _, installationID := range []string{"installation-a", "installation-b"} {
		wait.Add(1)
		go func(value string) {
			defer wait.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tx, err := pool.Begin(ctx)
			if err != nil {
				results <- false
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			var usedAt *time.Time
			if err := tx.QueryRow(ctx, `SELECT used_at FROM activation_codes WHERE id=$1 FOR UPDATE`, codeID).Scan(&usedAt); err != nil || usedAt != nil {
				results <- false
				return
			}
			_, err = tx.Exec(ctx, `UPDATE activation_codes SET used_at=NOW(),installation_id=$2 WHERE id=$1`, codeID, value)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err == nil
		}(installationID)
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	for value := range results {
		if value {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("并发兑换成功数应为 1，实际为 %d", successes)
	}
}

func TestNodeIDUniqueBindingRejectsSecondDevice(t *testing.T) {
	pool := testPool(t)
	for index, values := range [][3]string{
		{"20000000-0000-4000-8000-000000000001", "install-1", "mesh//one"},
		{"20000000-0000-4000-8000-000000000002", "install-2", "mesh//two"},
	} {
		if _, err := pool.Exec(context.Background(), `INSERT INTO devices(id,installation_id,mesh_id,name,architecture) VALUES($1,$2,$3,$4,'amd64')`, values[0], values[1], values[2], "device"+string(rune('1'+index))); err != nil {
			t.Fatal(err)
		}
	}
	_, err := pool.Exec(context.Background(), `UPDATE devices SET node_id='node//same' WHERE installation_id='install-1'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `UPDATE devices SET node_id='node//same' WHERE installation_id='install-2'`)
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("第二台设备绑定同一 NodeID 必须触发唯一约束，得到 %v", err)
	}
}
