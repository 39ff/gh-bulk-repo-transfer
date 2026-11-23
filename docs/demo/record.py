# Records a scripted terminal session (synthetic shell prompt + real ghbt runs)
# into a JSON timeline of [seconds, text] events.
import codecs, fcntl, json, os, pty, random, select, struct, subprocess, sys, termios, time

COLS, ROWS = 86, 26
binary, scenario, out = sys.argv[1], sys.argv[2], sys.argv[3]
random.seed(7)
events, t0 = [], time.time()
def now(): return round(time.time() - t0, 3)
def emit(s): events.append([now(), s])

PROMPT = "\x1b[1;36m~/code\x1b[0m \x1b[1;32m$\x1b[0m "
def type_cmd(cmd, pause=0.7):
    emit(PROMPT); time.sleep(pause)
    for ch in cmd:
        emit(ch); time.sleep(0.04 + random.random() * 0.05)
    time.sleep(0.45); emit("\r\n")

def run(args, steps, port):
    """Run ghbt in a pty. steps: (text to wait for or None, pause seconds, key bytes)."""
    import pyte
    screen = pyte.Screen(COLS, ROWS); stream = pyte.ByteStream(screen)
    env = dict(os.environ, TERM="xterm-256color", COLORTERM="truecolor", GH_TOKEN="demo",
               GITHUB_API_URL=f"http://127.0.0.1:{port}", LANG="en_US.UTF-8")
    env.pop("NO_COLOR", None)
    pid, fd = pty.fork()
    if pid == 0:
        os.execve(binary, [binary] + args, env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
    dec = codecs.getincrementaldecoder("utf-8")(errors="replace")
    alive = True
    def pump(seconds):
        nonlocal alive
        end = time.time() + seconds
        while alive and time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.02)
            if r:
                try: data = os.read(fd, 65536)
                except OSError: alive = False; return
                if not data: alive = False; return
                stream.feed(data)
                s = dec.decode(data)
                if s: emit(s)
    def screen_text(): return "\n".join(screen.display)
    for wait_text, pause, key in steps:
        deadline = time.time() + 15
        while wait_text and wait_text not in screen_text():
            if not alive or time.time() > deadline:
                raise SystemExit(f"timed out waiting for {wait_text!r}:\n" + screen_text())
            pump(0.05)
        pump(pause)
        os.write(fd, key)
    while alive:
        pump(0.2)
    os.waitpid(pid, 0)

srv = subprocess.Popen([sys.executable, "-I", "server.py"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
port = srv.stdout.readline().strip()
time.sleep(0.6)
DOWN, LEFT, ENTER, SPACE = b"\x1b[B", b"\x1b[D", b"\r", b" "

if scenario == "interactive":
    type_cmd("ghbt")
    steps = [("Move repositories FROM", 1.8, ENTER),          # alice (your account)
             ("Move repositories TO", 1.6, ENTER),             # alice-archive (owner)
             ("Select repositories", 1.6, SPACE)]              # jquery-playground
    for k in [DOWN, SPACE, DOWN, SPACE, DOWN, SPACE, DOWN, DOWN, SPACE, DOWN, DOWN, SPACE]:
        steps.append((None, 0.38, k))                          # hackathon, old-blog, todo-app, coding-test-prep, side-project-x
    steps += [(None, 1.0, ENTER),                              # done selecting
              ("Archive them after moving?", 1.0, LEFT),
              (None, 0.7, ENTER),                              # Yes
              ("repositories to alice-archive?", 2.8, LEFT),
              (None, 0.7, ENTER)]                              # Transfer
    run(["interactive", "--interval", "300ms"], steps, port)
    time.sleep(0.2); emit(PROMPT); time.sleep(3.5)
elif scenario == "cli":
    type_cmd("ghbt list --sort pushed --pushed-before 2021-01-01")
    run(["list", "--sort", "pushed", "--pushed-before", "2021-01-01"], [], port)
    time.sleep(2.4)
    type_cmd("ghbt transfer --to alice-archive --pushed-before 2021-01-01 --forks exclude")
    run(["transfer", "--to", "alice-archive", "--pushed-before", "2021-01-01", "--forks", "exclude", "--interval", "300ms"],
        [("[y/N]", 2.4, b"y"), (None, 0.5, b"\r")], port)
    time.sleep(0.2); emit(PROMPT); time.sleep(3.5)
emit("")
srv.stdin.close(); srv.wait()
json.dump(dict(cols=COLS, rows=ROWS, events=events), open(out, "w"))
print(f"{scenario}: {len(events)} events, {events[-1][0]:.1f}s")
