package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Vincentkeio/kokoro/internal/backup"
	"github.com/Vincentkeio/kokoro/internal/hub"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
	"log"
	"path/filepath"
	"time"
)

// serveHub reloads the Hub in-process after an import. This works under the
// unprivileged service account without granting access to systemctl or shells.
func serveHub(ctx context.Context, cfg *model.HubConfig) error {
	if err := backup.RecoverPending(cfg.DataDir); err != nil {
		return fmt.Errorf("恢复上次未完成的导入：%w", err)
	}
	var tx *backup.Transaction
	for {
		st, err := store.Open(filepath.Join(cfg.DataDir, "kokoro.db"))
		var h *hub.Hub
		if err == nil {
			if tx != nil {
				err = hub.ValidateRestoredData(ctx, st)
			}
			if err == nil {
				h, err = hub.New(cfg, st)
			}
		}
		if err != nil {
			if st != nil {
				st.Close()
			}
			if tx != nil {
				if e := tx.Rollback(); e != nil {
					return fmt.Errorf("导入初始化失败 %v，回退失败：%w", err, e)
				}
				tx = nil
				log.Printf("[backup] 导入初始化失败，已回退：%v", err)
				continue
			}
			return err
		}
		runCtx, cancel := context.WithCancel(ctx)
		maintenanceDone := make(chan struct{})
		go func() { defer close(maintenanceDone); runStoreMaintenance(runCtx, st) }()
		if tx != nil {
			h.SetReadyCallback(func() error {
				if err := tx.Commit(); err != nil {
					return err
				}
				tx = nil
				log.Printf("[backup] 备份导入完成")
				return nil
			})
		}
		log.Printf("[kokoro] Hub 启动 %s（数据目录 %s，版本 %s）", cfg.Listen, cfg.DataDir, Version)
		err = h.Run(runCtx)
		cancel()
		<-maintenanceDone
		st.Close()
		if tx != nil {
			if e := tx.Rollback(); e != nil {
				return fmt.Errorf("导入启动失败 %v，回退失败：%w", err, e)
			}
			tx = nil
			if ctx.Err() == nil {
				continue
			}
		}
		if !errors.Is(err, hub.ErrRestoreReady) {
			return err
		}
		tx, err = backup.Install(cfg.DataDir, h.PendingRestore())
		if err != nil {
			log.Printf("[backup] 导入失败，保留原面板：%v", err)
		}
	}
}
func runStoreMaintenance(ctx context.Context, st *store.Store) {
	compact := time.NewTicker(5 * time.Minute)
	retention := time.NewTicker(time.Hour)
	defer compact.Stop()
	defer retention.Stop()
	if err := st.AggregateRollups(); err != nil {
		log.Printf("[kokoro] 聚合失败：%v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-compact.C:
			if err := st.AggregateRollups(); err != nil {
				log.Printf("[kokoro] 聚合失败：%v", err)
			}
		case <-retention.C:
			if err := st.Prune(); err != nil {
				log.Printf("[kokoro] 清理失败：%v", err)
			}
		}
	}
}
