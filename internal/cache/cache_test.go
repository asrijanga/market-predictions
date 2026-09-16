package cache

import "testing"

func TestRoundTrip(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("unexpected hit for missing key")
	}
	if err := c.Put("k", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("k")
	if !ok || string(got) != "hello" {
		t.Fatalf("Get = %q, %v; want hello, true", got, ok)
	}
}

func TestNilCache(t *testing.T) {
	var c *Cache
	if _, ok := c.Get("k"); ok {
		t.Fatal("nil cache should never hit")
	}
	if err := c.Put("k", []byte("x")); err != nil {
		t.Fatalf("nil cache Put returned error: %v", err)
	}
}
