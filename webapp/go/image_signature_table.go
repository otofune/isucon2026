package isureal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// 初期画像の署名を期限ごとに事前計算したファイル。不変なので全台で共有できる。
// 1件は画像 ID 16バイトと署名32バイトで、ID の昇順に並ぶ。
const signatureTableDirectory = "/home/isucon/signatures"
const signatureRecordSize = 16 + 32

type signatureTable struct {
	exp  int64
	data []byte
}

var currentSignatureTable atomic.Pointer[signatureTable]

func signatureTablePath(exp int64) string {
	return filepath.Join(signatureTableDirectory, fmt.Sprintf("%d.bin", exp))
}

// loadSignatureTable はファイルがなければ何もしない。その場合は都度計算する。
func loadSignatureTable(exp int64) {
	file, err := os.Open(signatureTablePath(exp))
	if err != nil {
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.Size() == 0 || stat.Size()%signatureRecordSize != 0 {
		return
	}
	data, err := syscall.Mmap(int(file.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return
	}
	currentSignatureTable.Store(&signatureTable{exp: exp, data: data})
}

func lookupPrecomputedSignature(imageID string, exp int64) []byte {
	table := currentSignatureTable.Load()
	if table == nil || table.exp != exp {
		return nil
	}
	id, err := uuid.Parse(imageID)
	if err != nil {
		return nil
	}
	count := len(table.data) / signatureRecordSize
	index := sort.Search(count, func(i int) bool {
		return bytes.Compare(table.data[i*signatureRecordSize:i*signatureRecordSize+16], id[:]) >= 0
	})
	if index == count {
		return nil
	}
	record := table.data[index*signatureRecordSize : (index+1)*signatureRecordSize]
	if !bytes.Equal(record[:16], id[:]) {
		return nil
	}
	return record[16:]
}

// PrecomputeSignatures はローカルの初期画像ストアから全画像の署名を計算する。
func PrecomputeSignatures(config *Config, workers int) (string, int, error) {
	db, err := sqlx.Open("pgx", config.LocalDatabaseDSN())
	if err != nil {
		return "", 0, err
	}
	defer db.Close()
	exp := currentImageURLExpiry()
	rows, err := db.QueryxContext(context.Background(), `SELECT i.id, u.secret_key_hash FROM image_objects i JOIN users u ON u.id = i.owner_user_id`)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	type job struct {
		id  string
		key []byte
	}
	keys := map[string][]byte{}
	var jobs []job
	for rows.Next() {
		var id, secretKeyHash string
		if err := rows.Scan(&id, &secretKeyHash); err != nil {
			return "", 0, err
		}
		key, ok := keys[secretKeyHash]
		if !ok {
			if key, err = imageSigningKey(secretKeyHash); err != nil {
				return "", 0, err
			}
			keys[secretKeyHash] = key
		}
		jobs = append(jobs, job{id: id, key: key})
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	output := make([]byte, len(jobs)*signatureRecordSize)
	var next atomic.Int64
	var failed atomic.Value
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(jobs) {
					return
				}
				id, err := uuid.Parse(jobs[i].id)
				if err != nil {
					failed.Store(err)
					return
				}
				record := output[i*signatureRecordSize : (i+1)*signatureRecordSize]
				copy(record[:16], id[:])
				copy(record[16:], computeImageSignature(jobs[i].key, jobs[i].id, exp))
				if i%100000 == 0 {
					fmt.Fprintln(os.Stderr, "signed", i, "/", len(jobs))
				}
			}
		}()
	}
	wg.Wait()
	if err, ok := failed.Load().(error); ok {
		return "", 0, err
	}
	sortSignatureRecords(output)
	if err := os.MkdirAll(signatureTableDirectory, 0o755); err != nil {
		return "", 0, err
	}
	path := signatureTablePath(exp)
	if err := os.WriteFile(path+".tmp", output, 0o644); err != nil {
		return "", 0, err
	}
	return path, len(jobs), os.Rename(path+".tmp", path)
}

type signatureRecords []byte

func (r signatureRecords) Len() int { return len(r) / signatureRecordSize }
func (r signatureRecords) Less(i, j int) bool {
	return bytes.Compare(r[i*signatureRecordSize:i*signatureRecordSize+16], r[j*signatureRecordSize:j*signatureRecordSize+16]) < 0
}
func (r signatureRecords) Swap(i, j int) {
	var buffer [signatureRecordSize]byte
	a := r[i*signatureRecordSize : (i+1)*signatureRecordSize]
	b := r[j*signatureRecordSize : (j+1)*signatureRecordSize]
	copy(buffer[:], a)
	copy(a, b)
	copy(b, buffer[:])
}

func sortSignatureRecords(data []byte) {
	sort.Sort(signatureRecords(data))
}
