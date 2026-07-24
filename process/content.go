package process

// ContentReader resolves a federated file's content by its path (relative to the
// federated tree). When a process is built with one, its read_file consults the verse
// (the docd content controller) instead of the local filesystem — so a process reads
// through the verse rather than a shared disk, which is what lets a validator run
// somewhere other than where the files live. Returns the content and whether found.
type ContentReader interface {
	ReadContent(path string) (string, bool)
}
