// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package rootfile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// moves.tf has the moved blocks sorted by where they move to, keys and
// all, and reads back the same.
func TestWriteAndReadMoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got, err := ReadMoves(dir); err != nil || got != nil {
		t.Fatalf("without moves.tf: %+v, %v", got, err)
	}
	moved := []Move{
		{From: "aws_subnet.a", To: `module.subnets.aws_subnet.this["a"]`},
		{From: "aws_s3_bucket.logs", To: "module.logs.aws_s3_bucket.this[0]"},
	}
	if err := WriteMoves(dir, moved, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := `moved {
  from = aws_s3_bucket.logs
  to   = module.logs.aws_s3_bucket.this[0]
}

moved {
  from = aws_subnet.a
  to   = module.subnets.aws_subnet.this["a"]
}
`
	if got, err := os.ReadFile(filepath.Join(dir, MovesFileName)); err != nil || string(got) != want {
		t.Errorf("got:\n%s\n%v\nwant:\n%s", got, err, want)
	}
	got, err := ReadMoves(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Move{moved[1], moved[0]}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if err := WriteMoves(dir, nil, nil, nil); err != nil {
		t.Errorf("without moves: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, MovesFileName)); err == nil {
		t.Error("without moves: moves.tf is still there, want it removed")
	}
}
