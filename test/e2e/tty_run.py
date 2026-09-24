#!/usr/bin/env python3
"""Runs a command in a pseudo-terminal and answers prompts the way a person
at the keyboard would, waiting until each prompt appears before typing.

  tty_run.py --answer 'Proceed? [y/N]=y' -- ssh -tt host 'curl ... | sudo sh'

Output is copied to stdout with CRLF line endings turned into LF. The exit
status is the command's.
"""
import argparse
import os
import pty
import select
import sys
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--answer", action="append", default=[], help="PROMPT=REPLY, answered once, in order")
    p.add_argument("--timeout", type=int, default=1200)
    p.add_argument("cmd", nargs=argparse.REMAINDER)
    a = p.parse_args()
    cmd = a.cmd[1:] if a.cmd[:1] == ["--"] else a.cmd
    answers = [tuple(x.split("=", 1)) for x in a.answer]
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)
    seen = b""
    deadline = time.time() + a.timeout
    while time.time() < deadline:
        ready, _, _ = select.select([fd], [], [], 1)
        if not ready:
            continue
        try:
            data = os.read(fd, 4096)
        except OSError:
            break
        if not data:
            break
        sys.stdout.buffer.write(data.replace(b"\r\n", b"\n"))
        sys.stdout.flush()
        seen += data
        if answers and answers[0][0].encode() in seen:
            time.sleep(0.5)
            os.write(fd, answers[0][1].encode() + b"\r")
            answers.pop(0)
            seen = b""
    else:
        os.kill(pid, 9)
        print(f"\ntty_run: timed out after {a.timeout} s", file=sys.stderr)
    _, status = os.waitpid(pid, 0)
    if answers:
        print(f"\ntty_run: prompt never appeared: {answers[0][0]!r}", file=sys.stderr)
        sys.exit(2)
    sys.exit(os.waitstatus_to_exitcode(status))


if __name__ == "__main__":
    main()
