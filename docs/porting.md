# Notes for anyone porting Wings elsewhere

Part of [Pterodactyl on Mac](../README.md).

## Notes for anyone porting Wings elsewhere

The Docker code was not the hard part. The Docker SDK is a pure-Go HTTP client
and cross-compiles to darwin untouched. The work was in `internal/ufs`, the
sandboxed filesystem layer, whose files are tagged `//go:build unix` but use
Linux-only syscalls:

- **`/proc/self/fd/N` does not exist on macOS.** Wings uses it to confirm where a
  descriptor actually landed after opening a multi-component path, since `O_NOFOLLOW`
  only guards the final component, so an intermediate symlink could otherwise
  escape the sandbox. The macOS equivalent is `fcntl(F_GETPATH)`.
- `F_GETPATH` returns **firmlink-resolved** paths, so a base directory under
  `/var`, `/tmp` or `/etc` comes back as `/private/...` and every sandbox check
  fails closed. The base is resolved once and results are translated back.
- There is no `openat2`/`RESOLVE_BENEATH` on XNU, so darwin takes the
  pre-5.6-kernel `openat` path that validates in userspace. `NewUnixFS` clamps
  the request so a caller asking for openat2 cannot get a filesystem where every
  operation returns `ENOSYS`.
- **There is no `O_PATH` either.** Since v1.13.5 Wings describes the target of a
  symlink by opening it with `O_PATH`, checking where it landed, and calling
  `fstat`, so that `Stat` cannot be used to look at a file outside the server
  root. `O_PATH` reaches a file without running its open routine. Any open
  darwin offers does run it, on whatever the link points at: a FIFO gains a
  reader, a device has its driver opened, and only then is it known to be
  outside the root. So darwin follows the chain of links itself, opening only
  the directory that holds each name and describing the name with
  `fstatat(AT_SYMLINK_NOFOLLOW)`.
- The chmod fallback for kernels without `fchmodat2` goes through an `O_PATH`
  descriptor and procfs on Linux. darwin's `fchmodat` has always accepted
  `AT_SYMLINK_NOFOLLOW`, so its `fchmodNoFollow` needs neither.
- Don't reach for `x/sys/unix.Getdirentries` on darwin. It emulates `getdents`
  by re-opening the directory and skipping N entries per call, which is O(n²).
  Use `fdopendir` (via `os.File.ReadDir`), on a duplicate of the descriptor
  wrapped with `os.NewFile`.
- **That is only right from Go 1.25.8 and 1.26.1.** A file made by `os.NewFile`
  has a label, not a path that leads back to the directory. APFS and HFS+ give
  every entry a type, but a network or FUSE volume may leave one untyped
  (`DT_UNKNOWN`), and then `ReadDir` has to stat it. Older releases did that
  with `lstat(f.Name() + "/" + entry)`, which is looked up in the process's
  working directory: the entry is dropped when nothing is there, and takes the
  type of whatever is. Newer ones call `fstatat` on the descriptor. `go.mod`
  requires 1.26.8, so this tree cannot be built with an affected release, and
  `TestOSDescribesEntriesThroughDescriptor` fails if a later one goes back. A
  port with a lower floor has to read the names with `Readdirnames` and
  `fstatat` each one itself. `DirEntry.Info` on what `ReadDir` returns still
  goes by path in every release, so use only `Name` and `Type`.

Process accounting for the Panel's graphs uses `proc_info(2)`
(`PROC_PIDTASKINFO` / `PROC_PIDTBSDINFO`) directly, so there is no cgo
dependency, and it sums across the process group so a shell wrapper doesn't
under-report.
