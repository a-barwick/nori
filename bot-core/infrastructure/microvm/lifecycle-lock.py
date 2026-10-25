"""Hold the shared local lifecycle lock across exec and nested wrapper calls."""

import fcntl
import os
from pathlib import Path
import sys


lock_dir = Path.home() / ".local/state/nori/bot-111549/terraform"
lock_path = lock_dir / "lifecycle.lock"
fd_env = "NORI_LIFECYCLE_LOCK_FD"


def inherited_lock():
    try:
        fd = int(os.environ.get(fd_env, ""))
        if fd < 3 or not os.path.samestat(os.fstat(fd), lock_path.stat()):
            return None
        # A nested call shares the same open file description, so this succeeds
        # without releasing or waiting for its parent's lock.
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return fd
    except (ValueError, OSError):
        return None


fd = inherited_lock()
if sys.argv[1:] == ["--held"]:
    sys.exit(0 if fd is not None else 1)
if len(sys.argv) < 2:
    sys.exit("Usage: lifecycle-lock.py COMMAND [ARGUMENTS]")
if fd is None:
    os.umask(0o077)
    lock_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print("Waiting for the Nori cloud lifecycle lock...", file=sys.stderr, flush=True)
        fcntl.flock(fd, fcntl.LOCK_EX)
os.set_inheritable(fd, True)
os.environ[fd_env] = str(fd)
# No supervisor process or unlink-on-exit race: the OS retains the advisory lock
# through shell EXIT cleanup and releases it when the last inheriting process exits.
os.execvp(sys.argv[1], sys.argv[1:])
