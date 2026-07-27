package mpd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStickerStorePersistenceAndMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stickers.json")
	s, err := OpenStickerStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Set("netease://song/1", "rating", "9.5"); err != nil {
		t.Fatal(err)
	}
	if err = s.Set("netease://song/1", "note", "arbitrary value"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err = s.Set("netease://song/1", "repeated", string(rune('a'+i))); err != nil {
			t.Fatalf("repeated overwrite %d: %v", i, err)
		}
	}
	reopened, err := OpenStickerStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.Get("netease://song/1", "rating"); !ok || got != "9.5" {
		t.Fatalf("reopened value = %q, %v", got, ok)
	}
	if !stickerMatch("9.5", ">", "8") || !stickerMatch("arbitrary value", "contains", "value") || stickerMatch("9.5", "<=", "8") {
		t.Fatal("sticker operators produced incorrect results")
	}
	key := "rating"
	if err = reopened.Delete("netease://song/1", &key); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get("netease://song/1", key); ok {
		t.Fatal("deleted sticker remains")
	}
	if err = reopened.Delete("netease://song/1", nil); err != nil {
		t.Fatal(err)
	}
	if got := reopened.List("netease://song/1"); len(got) != 0 {
		t.Fatalf("delete all left %v", got)
	}
	if err = os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenStickerStore(path); err == nil {
		t.Fatal("malformed sticker store was accepted")
	}
}
