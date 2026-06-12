// SPDX-FileCopyrightText: William Moreno Reyes CP | MBA
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/admiral-project/admiral/admirald/pkg/admiral"
)

type outbox struct {
	dir string
}

func newOutbox(dir string) *outbox {
	if dir == "" {
		dir = "/var/lib/admiral/outbox"
	}
	return &outbox{dir: dir}
}

func (o *outbox) enqueue(result admiral.TaskResult) error {
	if err := os.MkdirAll(o.dir, 0700); err != nil {
		return fmt.Errorf("create outbox dir: %w", err)
	}
	name := fmt.Sprintf("%d-%s-%s.json", time.Now().UTC().UnixNano(), result.OperationID, result.TaskID)
	name = filepath.Clean(name)
	name = strings.ReplaceAll(name, "..", "")
	name = strings.ReplaceAll(name, "/", "")
	path := filepath.Join(o.dir, name)
	body, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal task result: %w", err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		return fmt.Errorf("write outbox item: %w", err)
	}
	return nil
}

func (o *outbox) flush(send func(admiral.TaskResult) error) error {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read outbox dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		files = append(files, filepath.Join(o.dir, e.Name()))
	}
	sort.Strings(files)
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var result admiral.TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			_ = os.Remove(path)
			continue
		}
		if err := send(result); err != nil {
			return err
		}
		_ = os.Remove(path)
	}
	return nil
}
