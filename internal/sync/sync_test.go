package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []fakeCall
	// keyed by "name args[0] args[1]..."
	responses map[string][]byte
}

type fakeCall struct {
	name  string
	args  []string
	stdin []byte
}

func (f *fakeRunner) Run(name string, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	key := name + " " + strings.Join(args, " ")
	for k, v := range f.responses {
		if strings.Contains(key, k) {
			return v, nil
		}
	}
	return []byte(""), nil
}

func TestSyncScripts_SkipsWhenHashesMatch(t *testing.T) {
	content := []byte("#!/bin/bash\necho hi\n")
	h := sha256.Sum256(content)
	hashHex := hex.EncodeToString(h[:])

	r := &fakeRunner{
		responses: map[string][]byte{
			"shasum": []byte(hashHex + "\n"),
		},
	}
	err := SyncScripts("myserver", []Script{{Name: "s.sh", Content: content}}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should only see a single ssh call (the hash check). No scp.
	for _, c := range r.calls {
		if c.name == "scp" {
			t.Errorf("unexpected scp call when hashes match: %+v", c)
		}
	}
}

func TestSyncScripts_UploadsWhenMissing(t *testing.T) {
	content := []byte("#!/bin/bash\necho hi\n")
	r := &fakeRunner{
		responses: map[string][]byte{
			"shasum": []byte("MISSING\n"),
		},
	}
	err := SyncScripts("myserver", []Script{{Name: "s.sh", Content: content}}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	sawScp := false
	sawChmod := false
	for _, c := range r.calls {
		if c.name == "scp" {
			sawScp = true
			if len(c.args) < 2 || !strings.HasSuffix(c.args[1], ":~/bin/s.sh") {
				t.Errorf("scp target wrong: %v", c.args)
			}
		}
		if c.name == "ssh" {
			for _, a := range c.args {
				if strings.Contains(a, "chmod +x") {
					sawChmod = true
				}
			}
		}
	}
	if !sawScp {
		t.Error("expected scp call when remote file MISSING")
	}
	if !sawChmod {
		t.Error("expected chmod +x after upload")
	}
}

func TestSyncScripts_UploadsWhenHashDiffers(t *testing.T) {
	content := []byte("#!/bin/bash\necho hi\n")
	r := &fakeRunner{
		responses: map[string][]byte{
			"shasum": []byte("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n"),
		},
	}
	err := SyncScripts("myserver", []Script{{Name: "s.sh", Content: content}}, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	sawScp := false
	for _, c := range r.calls {
		if c.name == "scp" {
			sawScp = true
		}
	}
	if !sawScp {
		t.Error("expected scp call when hashes differ")
	}
}
