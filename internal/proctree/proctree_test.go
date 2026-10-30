package proctree_test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/proctree"
)

// The child starts a grandchild that inherits its stdout, so the pipe reaches
// EOF only when both have exited.
func TestTreeProcess(t *testing.T) {
	switch os.Getenv("BL_PROCTREE") {
	case "child":
		grandchild := exec.Command(os.Args[0], "-test.run=^TestTreeProcess$")
		grandchild.Env = append(os.Environ(), "BL_PROCTREE=grandchild")
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		time.Sleep(time.Minute)
	case "grandchild":
		time.Sleep(time.Minute)
	}
}

// Regression: a timed-out source adapter must not leave its children running.
func TestKillEndsWholeTree(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTreeProcess$")
	cmd.Env = append(os.Environ(), "BL_PROCTREE=child")
	cmd.Stdout = w
	tree, err := proctree.Start(cmd)
	_ = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Kill()
	reader := bufio.NewReader(r)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child: %q %v", line, err)
	}
	tree.Kill()
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, reader); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("a process in the tree survived Kill")
	}
	_ = cmd.Wait()
}
