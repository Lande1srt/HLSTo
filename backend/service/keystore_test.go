package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestKeyStore_PutAndIndex(t *testing.T) {
	withTempWd(t)
	dir := filepath.Join(mustGetwd(t), keyStoreDirName)

	key := []byte("0123456789abcdef")
	path, err := defaultKeyStore.put("id-1", key, "movie.key")
	if err != nil {
		t.Fatalf("put 失败: %v", err)
	}

	// 密钥以 ID 命名
	if filepath.Base(path) != "id-1.key" {
		t.Fatalf("密钥文件名 = %q, want id-1.key", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(key) {
		t.Fatalf("密钥内容异常: %v", err)
	}

	// index.json 结构
	index := readKeyIndex(t, dir)
	if index.Version != keyStoreVersion {
		t.Errorf("index.version = %d, want %d", index.Version, keyStoreVersion)
	}
	if len(index.Keys) != 1 {
		t.Fatalf("index 记录数 = %d, want 1", len(index.Keys))
	}
	rec := index.Keys[0]
	if rec.ID != "id-1" || rec.FileName != "movie.key" {
		t.Errorf("索引记录异常: %+v", rec)
	}
	if rec.CreatedAt == "" {
		t.Errorf("createdAt 为空")
	}

	// 查询接口
	if got := defaultKeyStore.fileName("id-1"); got != "movie.key" {
		t.Errorf("fileName 查询 = %q", got)
	}
	if got := defaultKeyStore.fileName("missing"); got != "" {
		t.Errorf("不存在的 ID 应返回空，实际 %q", got)
	}
}

func TestKeyStore_Upsert(t *testing.T) {
	withTempWd(t)
	dir := filepath.Join(mustGetwd(t), keyStoreDirName)

	// 非法文件名应被清洗为 enc.key
	if _, err := defaultKeyStore.put("id-1", []byte("0123456789abcdef"), `a/b".key`); err != nil {
		t.Fatalf("首次 put 失败: %v", err)
	}
	if got := defaultKeyStore.fileName("id-1"); got != "enc.key" {
		t.Fatalf("非法文件名应回退 enc.key，实际 %q", got)
	}

	// 同 ID 再次 put（重试场景）：密钥覆盖，索引仍只有一条，文件名更新
	if _, err := defaultKeyStore.put("id-1", []byte("ffffffffffffffff"), "new.key"); err != nil {
		t.Fatalf("二次 put 失败: %v", err)
	}
	index := readKeyIndex(t, dir)
	if len(index.Keys) != 1 {
		t.Fatalf("upsert 后记录数 = %d, want 1", len(index.Keys))
	}
	if index.Keys[0].FileName != "new.key" {
		t.Errorf("upsert 后文件名 = %q", index.Keys[0].FileName)
	}
	d, _ := os.ReadFile(filepath.Join(dir, "id-1.key"))
	if string(d) != "ffffffffffffffff" {
		t.Errorf("upsert 后密钥内容未更新")
	}

	// 另一个 ID：两条记录
	if _, err := defaultKeyStore.put("id-2", []byte("1111111111111111"), "enc.key"); err != nil {
		t.Fatalf("put id-2 失败: %v", err)
	}
	if len(readKeyIndex(t, dir).Keys) != 2 {
		t.Fatalf("记录数应为 2")
	}
}

func TestKeyStore_Remove(t *testing.T) {
	withTempWd(t)
	dir := filepath.Join(mustGetwd(t), keyStoreDirName)

	// 移除不存在的 ID：无错误
	if err := defaultKeyStore.remove("nobody"); err != nil {
		t.Errorf("移除不存在的 ID 不应报错: %v", err)
	}

	for _, id := range []string{"id-1", "id-2"} {
		if _, err := defaultKeyStore.put(id, []byte("0123456789abcdef"), "enc.key"); err != nil {
			t.Fatalf("put %s 失败: %v", id, err)
		}
	}

	if err := defaultKeyStore.remove("id-1"); err != nil {
		t.Fatalf("remove 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "id-1.key")); !os.IsNotExist(err) {
		t.Errorf("id-1.key 应已删除")
	}
	if len(readKeyIndex(t, dir).Keys) != 1 {
		t.Errorf("索引应剩 1 条")
	}

	// 删除最后一条：密钥与 index.json 均移除
	if err := defaultKeyStore.remove("id-2"); err != nil {
		t.Fatalf("remove 最后一条失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "id-2.key")); !os.IsNotExist(err) {
		t.Errorf("id-2.key 应已删除")
	}
	if _, err := os.Stat(filepath.Join(dir, keyStoreIndexName)); !os.IsNotExist(err) {
		t.Errorf("无剩余密钥时 index.json 应移除")
	}
}

func TestKeyStore_ConcurrentPut(t *testing.T) {
	withTempWd(t)
	dir := filepath.Join(mustGetwd(t), keyStoreDirName)

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "id-" + string(rune('a'+i))
			_, err := defaultKeyStore.put(id, []byte("0123456789abcdef"), "enc.key")
			if err != nil {
				t.Errorf("并发 put %s 失败: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	index := readKeyIndex(t, dir)
	if len(index.Keys) != n {
		t.Fatalf("并发后索引记录数 = %d, want %d", len(index.Keys), n)
	}
	seen := map[string]bool{}
	for _, r := range index.Keys {
		if seen[r.ID] {
			t.Fatalf("索引出现重复 ID: %s", r.ID)
		}
		seen[r.ID] = true
		if _, err := os.Stat(filepath.Join(dir, r.ID+".key")); err != nil {
			t.Fatalf("密钥文件 %s 缺失: %v", r.ID, err)
		}
	}
}

func TestOriginalKeyFileName(t *testing.T) {
	cases := map[string]string{
		"https://x.com/a/my.key": "my.key",
		"https://x.com/path/":    "path", // path.Base 先 Clean，尾斜杠后仍取到末段
		"https://x.com":          "enc.key",
		"":                       "enc.key",
		"://broken":              "enc.key",
	}
	for in, want := range cases {
		if got := originalKeyFileName(in); got != want {
			t.Errorf("originalKeyFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeKeyFileName(t *testing.T) {
	cases := map[string]string{
		"enc.key":    "enc.key",
		" a.key ":    "a.key",
		`a".key`:     "enc.key", // 引号
		"a/b.key":    "enc.key", // 路径分隔
		"a\\b.key":   "enc.key",
		"a\x00b.key": "enc.key", // 控制字符
		"a\nb.key":   "enc.key",
	}
	for in, want := range cases {
		if got := sanitizeKeyFileName(in); got != want {
			t.Errorf("sanitizeKeyFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func readKeyIndex(t *testing.T, dir string) hlsKeyIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, keyStoreIndexName))
	if err != nil {
		t.Fatalf("读取 index.json 失败: %v", err)
	}
	var index hlsKeyIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("解析 index.json 失败: %v", err)
	}
	return index
}
