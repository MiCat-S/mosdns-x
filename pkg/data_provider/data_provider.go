/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package data_provider

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/pmkol/mosdns-x/pkg/safe_close"
)

type DataManager struct {
	pm sync.RWMutex
	ps map[string]*DataProvider
}

type DataListener interface {
	Update(newData []byte) error
}

func NewDataManager() *DataManager {
	return &DataManager{
		ps: make(map[string]*DataProvider),
	}
}

func (m *DataManager) AddDataProvider(name string, p *DataProvider) {
	m.pm.Lock()
	defer m.pm.Unlock()
	m.ps[name] = p
}

func (m *DataManager) GetDataProvider(name string) *DataProvider {
	m.pm.RLock()
	defer m.pm.RUnlock()
	return m.ps[name]
}

type DataProviderConfig struct {
	Tag        string `yaml:"tag"`
	File       string `yaml:"file"`
	AutoReload bool   `yaml:"auto_reload"`
}

type DataProvider struct {
	logger     *zap.Logger
	file       string
	autoReload bool

	lm        sync.Mutex
	listeners map[DataListener]struct{}

	sc *safe_close.SafeClose
}

func NewDataProvider(lg *zap.Logger, cfg DataProviderConfig) (*DataProvider, error) {
	dp := new(DataProvider)
	dp.logger = lg
	dp.file = cfg.File
	dp.autoReload = cfg.AutoReload

	dp.sc = safe_close.NewSafeClose()

	if err := dp.init(); err != nil {
		return nil, err
	}
	return dp, nil
}

func (ds *DataProvider) init() error {
	_, err := ds.loadFromDisk()
	if err != nil {
		return err
	}

	if ds.autoReload {
		if err := ds.startFsWatcher(); err != nil {
			return fmt.Errorf("failed to start fs watcher, %w", err)
		}
	}
	return nil
}

func (ds *DataProvider) Close() {
	ds.sc.Done()
	ds.sc.CloseWait()
}

// LoadAndAddListener loads the DataListener, returns any error that occurs, and
// add this DataListener to this DataProvider.
func (ds *DataProvider) LoadAndAddListener(l DataListener) error {
	b, err := ds.GetData()
	if err != nil {
		return err
	}

	if err := l.Update(b); err != nil {
		return err
	}

	ds.lm.Lock()
	if ds.listeners == nil {
		ds.listeners = make(map[DataListener]struct{})
	}
	ds.listeners[l] = struct{}{}
	ds.lm.Unlock()
	return nil
}

func (ds *DataProvider) DeleteListener(l DataListener) {
	ds.lm.Lock()
	defer ds.lm.Unlock()
	delete(ds.listeners, l)
}

func (ds *DataProvider) GetData() ([]byte, error) {
	return os.ReadFile(ds.file)
}

// pushData notify the notifier and trigger all listeners.
func (ds *DataProvider) pushData(newData []byte) {
	ds.lm.Lock()
	ls := make([]DataListener, 0, len(ds.listeners))
	for listener := range ds.listeners {
		ls = append(ls, listener)
	}
	ds.lm.Unlock()

	for _, l := range ls {
		if err := l.Update(newData); err != nil {
			ds.logger.Error(
				"failed to update data listener",
				zap.Error(err),
			)
		}
	}
}

func (ds *DataProvider) loadFromDisk() ([]byte, error) {
	return os.ReadFile(ds.file)
}

// startFsWatcher watches the parent directory of ds.file and reloads the file
// after changes. The directory is watched instead of the file itself, so
// replacements via rename (as done by most editors and deploy tools) keep
// working after the old inode is gone.
func (ds *DataProvider) startFsWatcher() error {
	file := filepath.Clean(ds.file)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(filepath.Dir(file)); err != nil {
		w.Close()
		return err
	}

	ds.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
		defer done()
		defer w.Close()

		// The timer is owned by this goroutine only.
		var delayReloadTimer *time.Timer
		var delayReloadC <-chan time.Time
		defer func() {
			if delayReloadTimer != nil {
				delayReloadTimer.Stop()
			}
		}()

		for {
			select {
			case e, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Clean(e.Name) != file ||
					!(e.Has(fsnotify.Create) || e.Has(fsnotify.Write) || e.Has(fsnotify.Rename) || e.Has(fsnotify.Remove)) {
					continue
				}
				ds.logger.Info(
					"fs event",
					zap.Stringer("event", e.Op),
					zap.String("file", e.Name),
				)

				if delayReloadTimer == nil {
					delayReloadTimer = time.NewTimer(time.Second)
				} else {
					if !delayReloadTimer.Stop() {
						select {
						case <-delayReloadTimer.C:
						default:
						}
					}
					delayReloadTimer.Reset(time.Second)
				}
				delayReloadC = delayReloadTimer.C

			case <-delayReloadC:
				delayReloadC = nil
				ds.reloadFromDisk(closeSignal)

			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				ds.logger.Error("fs notify error", zap.Error(err))

			case <-closeSignal:
				return
			}
		}
	})
	return nil
}

// reloadFromDisk reloads ds.file and pushes it to listeners, unless closeSignal
// has been received. A missing file is logged and skipped, the next fs event
// will trigger another reload.
func (ds *DataProvider) reloadFromDisk(closeSignal <-chan struct{}) {
	if _, err := os.Stat(ds.file); err != nil {
		ds.logger.Info(
			"file is not available, waiting for the next fs event",
			zap.String("file", ds.file),
			zap.Error(err),
		)
		return
	}

	ds.logger.Info(
		"reloading file",
		zap.String("file", ds.file),
	)
	v, err := ds.loadFromDisk()
	if err != nil {
		ds.logger.Error(
			"failed to reload file",
			zap.String("file", ds.file),
			zap.Error(err),
		)
		return
	}

	select {
	case <-closeSignal:
		return
	default:
	}
	ds.logger.Info(
		"file reloaded",
		zap.String("file", ds.file),
	)
	ds.pushData(v)
}
