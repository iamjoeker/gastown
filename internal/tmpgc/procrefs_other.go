//go:build !linux

package tmpgc

// liveReferences has no procfs to read on this platform. Returning the refusal
// makes every candidate report as refused rather than reclaimable: the sweep
// still measures what is stranded, but removes nothing without evidence.
func liveReferences([]string) (Evidence, error) {
	return Evidence{}, ErrNoProcEvidence
}
