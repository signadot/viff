package task

// ContentReader resolves a federated file's content by its path (relative to the
// federated tree). When a task is built with one, its read_file consults the verse
// (the docd content controller) instead of the local filesystem — so a task reads
// through the verse rather than a shared disk, which is what lets a validator run
// somewhere other than where the files live.
//
// ok reports whether the file exists; err reports whether the lookup could be performed.
// The read crosses the network and traverses a controller mount, so it can fail while the
// file is sitting there — and an agent told "no such file" when the truth is "I could not
// ask" will conclude the file needs creating. That is a worse outcome than an error.
type ContentReader interface {
	ReadContent(path string) (string, bool, error)
}
