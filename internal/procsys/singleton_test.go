package procsys

import "testing"

// TestAcquireSingletonExcludesSecondHolder covers the two-process rule the app
// relies on: one data directory may be owned by exactly one process. Both
// implementations must also release the claim when the holder goes away, or a
// crash would need manual cleanup before the next start.
func TestAcquireSingletonExcludesSecondHolder(t *testing.T) {
	dir := t.TempDir()
	release, ok, err := AcquireSingleton(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !ok {
		t.Fatal("first acquire must succeed")
	}

	release2, ok2, err2 := AcquireSingleton(dir)
	if err2 != nil {
		t.Fatalf("second acquire returned an error instead of a clean no: %v", err2)
	}
	if ok2 {
		release2()
		t.Fatal("second acquire must be refused while the first holder lives")
	}

	release()
	release3, ok3, err3 := AcquireSingleton(dir)
	if err3 != nil || !ok3 {
		t.Fatalf("acquire after release = %v, %v", ok3, err3)
	}
	release3()
}

func TestAcquireSingletonSeparatesDataDirs(t *testing.T) {
	releaseA, okA, err := AcquireSingleton(t.TempDir())
	if err != nil || !okA {
		t.Fatalf("acquire A = %v, %v", okA, err)
	}
	defer releaseA()
	releaseB, okB, err := AcquireSingleton(t.TempDir())
	if err != nil || !okB {
		t.Fatalf("a different data dir must not be blocked: %v, %v", okB, err)
	}
	releaseB()
}
