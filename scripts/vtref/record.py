#!/usr/bin/env python3
"""Record the raw PTY output of a program for a fixed time, then kill it.

usage: rec.py OUT COLS ROWS SECONDS -- prog [args...]
Every child is killed by process group before exit, so nothing leaks.
"""
import fcntl, os, pty, select, signal, struct, sys, termios, time

out, cols, rows, secs = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), float(sys.argv[4])
assert sys.argv[5] == "--"
argv = sys.argv[6:]

pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.environ["COLORTERM"] = "truecolor"
    os.environ["LANG"] = "C.UTF-8"
    os.execvp(argv[0], argv)

fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
buf = bytearray()
end = time.time() + secs
try:
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.1)
        if fd in r:
            try:
                d = os.read(fd, 65536)
            except OSError:
                break
            if not d:
                break
            buf += d
finally:
    try:
        os.killpg(os.getpgid(pid), signal.SIGKILL)
    except OSError:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    try:
        os.waitpid(pid, 0)
    except OSError:
        pass
    os.close(fd)

with open(out, "wb") as f:
    f.write(bytes(buf))
print(f"{out}: {len(buf)} bytes")
