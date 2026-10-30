package oauth

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func wantPrivate(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || !ownerOnlyDACL(sd) {
		t.Fatalf("%s is not owner-only: %v", path, err)
	}
}

// grantEveryone adds an Everyone read grant beside the owner's.
func grantEveryone(t *testing.T, path string) {
	t.Helper()
	user, err := currentUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// Mode bits mean nothing on Windows: a store others can read must be refused
// by its DACL, and one bl created must be accepted.
func TestWindowsPrivateDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "oauth")
	if err := makePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "record.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = protectFile(file); err != nil {
		t.Fatal(err)
	}
	if !privateDir(dir, nil) || !privateFile(file, nil) {
		t.Fatal("refused a directory or record bl created")
	}
	grantEveryone(t, path)
	if privateFile(file, nil) {
		t.Fatal("accepted a record others can read")
	}
	grantEveryone(t, dir)
	if privateDir(dir, nil) {
		t.Fatal("accepted a directory others can read")
	}
}
