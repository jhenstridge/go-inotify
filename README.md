# A Go binding for inotify

This is a fork of the since-deleted `golang.org/x/exp/inotify`
package. It breaks the API in an attempt to fix some reliability
problems:

1. inotify watches are attached to inodes rather than file paths. A
   single inode could have multiple file paths, or change its file
   path between when it is watched and when events are generated.

2. `AddWatch` now returns an `*Watch` representing the watch
   descriptor. For convenience, the watch stores the path it was
   created for.

3. There is no attempt to automatically use `IN_MASK_ADD`. The
   previous behaviour was unreliable, so you'll need to specify it
   manually if desired.

4. `Event` now includes a `Watch` field for its associated watch (or
   nil for events not associated with a watch like
   `IN_Q_OVERFLOW`). The `Name` field is no longer prefixed with the
   watch's path.

5. The `Watcher.Error` channel has been removed. Errors are now
   reported when `Close` is called.

6. The Watcher's mutex is now used to guard all access to the watches map.

7. When `RemoveWatch` is called, we don't completely forget about the
   watch until we receive an associated `IN_IGNORED` event. This way
   any queued events for the old watch can be correctly decoded.

8. Reads from the inotify instance are performed through an
   `*os.File`. This should take advantage of the Go runtime's IO
   polling system and stop the `readEvents` goroutine from tieing up
   an OS thread.
