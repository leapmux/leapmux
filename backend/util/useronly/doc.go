// Package useronly holds the Windows security rules that admit the user who
// runs this process and no one else:
//
//   - The security descriptors of a named pipe and of a directory. Each one
//     makes that user the owner and the primary group, and its access list
//     grants that user full access. Every other user and group gets no access.
//   - The check that the user owns a file or a directory.
//
// The package holds code for Windows only: on Unix the owner and the mode bits
// of a file state the same rules.
package useronly
