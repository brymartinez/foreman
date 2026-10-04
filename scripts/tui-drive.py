#!/usr/bin/env python3
"""Run the foreman dashboard in a pseudo-terminal and play steps against it.

  tui-drive.py <binary> [flags...] -- <step>...

The dashboard repaints the whole screen from the top on every frame, so the screen
is the text after the last cursor-home sequence with the escapes removed. Steps:

  refreshed     wait until a GitHub refresh has finished
  refresh       press r and wait for that refresh to finish
  wait=TEXT     wait until the screen shows TEXT
  keys=KEYS     press each key in KEYS; backslash escapes (\\r, \\x1b) work
  pick=TEXT     move the focused pane's cursor to the row that starts with TEXT
  sleep=SECS    wait
  print         write the screen to stdout
  quit          press q and wait for foreman to exit

A step that times out prints the screen to stderr and exits 1. After the last step
the driver quits foreman if it is still running.
"""
import codecs
import fcntl
import os
import re
import select
import struct
import subprocess
import sys
import termios
import time

TIMEOUT = float(os.environ.get("TUI_TIMEOUT", "20"))
ROWS, COLS = 60, 220
HOME = b"\x1b[H"
ESCAPES = re.compile(r"\x1b(\[[0-9;?]*[ -/]*[@-~]|[()][0-9A-Za-z]|[=>])")


class Dashboard:
    def __init__(self, argv):
        master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        self.proc = subprocess.Popen(argv, stdin=slave, stdout=slave, start_new_session=True)
        os.close(slave)
        self.fd = master
        self.out = b""
        self.read = 0

    def pump(self, wait):
        """Read what foreman wrote within wait seconds. It returns False once foreman exits."""
        ready, _, _ = select.select([self.fd], [], [], wait)
        if not ready:
            return True
        try:
            chunk = os.read(self.fd, 65536)
        except OSError:
            return False
        if not chunk:
            return False
        self.read += len(chunk)
        self.out = (self.out + chunk)[-1 << 20 :]
        return True

    def screen(self):
        frame = self.out[self.out.rfind(HOME) + len(HOME) :] if HOME in self.out else self.out
        return ESCAPES.sub("", frame.decode("utf-8", "replace")).replace("\r", "")

    def settle(self):
        """Read until foreman has been quiet for a moment, so the last frame is whole."""
        before = -1
        while before != self.read:
            before = self.read
            if not self.pump(0.1):
                return

    def until(self, ok, what):
        deadline = time.time() + TIMEOUT
        while time.time() < deadline:
            if ok():
                self.settle()
                if ok():
                    return
            if not self.pump(0.05) and not ok():
                self.fail(f"foreman exited while waiting for {what}")
        self.fail(f"timed out waiting for {what}")

    def press(self, key):
        before = self.read
        os.write(self.fd, key)
        deadline = time.time() + 2
        while self.read == before and time.time() < deadline and self.pump(0.05):
            pass
        # A key can draw more than one frame; let the burst finish.
        while self.pump(0.05) and select.select([self.fd], [], [], 0)[0]:
            pass

    def refreshed(self):
        self.until(lambda: "refreshed " in self.header() and "refreshing" not in self.header(), "a finished refresh")

    def header(self):
        return self.screen().split("\n", 1)[0]

    def pick(self, text):
        for _ in range(40):
            self.press(b"k")
        for _ in range(40):
            if re.search(r"(^|[\s│])> " + re.escape(text), self.screen(), re.M):
                return
            self.press(b"j")
        self.fail(f"no row starts with {text}")

    def quit(self):
        deadline = time.time() + TIMEOUT
        # The first q may only close the help or a prompt, so press esc and q again
        # while foreman is still up. Quitting itself can wait for a start in flight.
        keys = [b"q", b"\x1b", b"q"]
        while self.proc.poll() is None and time.time() < deadline:
            if keys:
                os.write(self.fd, keys.pop(0))
            end = time.time() + 1
            while self.proc.poll() is None and time.time() < end:
                self.pump(0.05)
        if self.proc.poll() is None:
            self.proc.kill()
            self.fail("foreman did not quit")
        while self.pump(0):
            if not select.select([self.fd], [], [], 0)[0]:
                break

    def fail(self, msg):
        sys.stderr.write(f"tui-drive: {msg}\n--- screen ---\n{self.screen()}\n")
        if self.proc.poll() is None:
            self.proc.kill()
        sys.exit(1)


def main():
    args = sys.argv[1:]
    split = args.index("--") if "--" in args else len(args)
    d = Dashboard(args[:split])
    for step in args[split + 1 :]:
        name, _, arg = step.partition("=")
        if name == "refreshed":
            d.refreshed()
        elif name == "refresh":
            d.press(b"r")
            d.refreshed()
        elif name == "wait":
            d.until(lambda: arg in d.screen(), repr(arg))
        elif name == "keys":
            for ch in codecs.decode(arg, "unicode_escape"):
                d.press(ch.encode())
        elif name == "pick":
            d.pick(arg)
        elif name == "sleep":
            end = time.time() + float(arg)
            while time.time() < end and d.pump(0.05):
                pass
        elif name == "print":
            d.settle()
            sys.stdout.write(d.screen() + "\n")
        elif name == "quit":
            d.quit()
        else:
            d.fail(f"unknown step {step}")
    d.quit()
    sys.exit(d.proc.returncode)


main()
