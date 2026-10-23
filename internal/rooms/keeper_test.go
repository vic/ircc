package rooms

import "testing"

// The live keeper path is tested in cmd/ircc (TestKeeperSigterm).
func TestSpawnKeeperBadSelf(t *testing.T) {
	for _, self := range []string{"", "/nonexistent/ircc"} {
		if p, err := SpawnKeeper(self); err == nil {
			p.Kill()
			p.Wait()
			t.Errorf("%q: want error", self)
		}
	}
}
