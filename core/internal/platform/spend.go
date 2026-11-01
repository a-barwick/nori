package platform

import (
	"nori/internal/spend"
	spendfirestore "nori/internal/spend/firestore"
)

// The shared development budget lives in Firestore. Registering here, like a database driver, keeps package spend free
// of any backend: a process that does not import platform fails closed when a request needs the budget.
func init() { spend.RegisterLedger(spendfirestore.Opener) }
