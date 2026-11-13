# easyswitch

easyswitch is a small program for Windows and macOS that moves Logitech
Easy-Switch devices (for example an MX Master mouse and an MX Keys keyboard)
between their channels 1, 2 and 3. It talks to the devices through a
Logi Bolt receiver. It needs nothing else installed and works next to
Logi Options+.

The goal is a service that lets Home Assistant say "switch mouse and keyboard
to channel 2". **That service is not built yet.** Today easyswitch is:

- a measuring tool (`diag`) that finds out how your receiver and devices
  behave, so the service can be built on real numbers instead of guesses, and
- a manual switch (`switch N`) you run yourself.

If you were asked to measure your devices, the section below is all you need.

## Measuring your devices

You will run a few commands, each of which writes a small text report, and
send the reports back. Do this on each computer your mouse and keyboard are
used with (for example a Windows PC on channel 1 and a Mac on channel 2), with
the devices connected to that computer at the time and its Bolt receiver
plugged in. Logi Options+ can keep running.

The steps, in order:

1. Download the program.
2. Quick check: `diag --quick`, under a minute.
3. Full measurement: `diag`, about 40 minutes.
4. Channel switch recording: `diag --watch 2m --idle=`, about 3 minutes.
5. Boot diag: one restart and one minute at the login screen.
6. Try `switch N`.
7. Send the reports back.

You can stop any measurement with Ctrl+C. The report written so far is kept
and ends with "interrupted".

### 1. Download the program

The programs are not signed with a paid Apple or Microsoft developer
certificate. What that means for you:

- Downloading with the PowerShell or Terminal commands below is the way
  least likely to bring up a warning.
- If you download it with a web browser instead, expect Windows SmartScreen
  or macOS Gatekeeper to block it until you allow it (see below for how).
- Windows security features such as Smart App Control or Microsoft Defender
  may still warn about or block an unsigned program, however it was
  downloaded. If a warning appears, the steps below say what to do; if
  Windows gives you no way to allow the program, stop there and tell whoever
  asked you to measure.

#### Windows

Open PowerShell (Start menu, type "PowerShell", press Enter). All Windows
commands in this guide are for PowerShell. Download the program into your
user folder:

```powershell
Invoke-WebRequest https://github.com/Orishas/easyswitch-mqtt/releases/latest/download/easyswitch-windows-amd64.exe -OutFile "$env:USERPROFILE\easyswitch.exe"
```

On a Windows computer with an ARM processor (for example a Snapdragon laptop),
use this one instead:

```powershell
Invoke-WebRequest https://github.com/Orishas/easyswitch-mqtt/releases/latest/download/easyswitch-windows-arm64.exe -OutFile "$env:USERPROFILE\easyswitch.exe"
```

Check that it runs:

```powershell
& "$env:USERPROFILE\easyswitch.exe" version
```

If you downloaded the file with a browser and Windows says "Windows protected
your PC", click "More info" and then "Run anyway". If Windows blocks the file
in another way, this removes the browser's download mark:

```powershell
Unblock-File "$env:USERPROFILE\easyswitch.exe"
```

#### macOS

Open Terminal (Applications > Utilities > Terminal). It starts in your home
folder. Download the program there and make it runnable (one file works on
both Apple silicon and Intel Macs):

```sh
curl -L -o easyswitch https://github.com/Orishas/easyswitch-mqtt/releases/latest/download/easyswitch-macos && chmod +x easyswitch
```

Check that it runs:

```sh
./easyswitch version
```

If you downloaded the file with a browser and macOS says it cannot check the
file for malicious software, open System Settings > Privacy & Security,
scroll down and click "Open Anyway" next to the message about easyswitch.
Simpler: download it with the `curl` command above instead.

macOS may ask whether Terminal may use **Input Monitoring**, because the
program talks to the receiver of a mouse and a keyboard. Allow it under
System Settings > Privacy & Security > Input Monitoring by turning on
Terminal (or the app you ran the program from), then run the command again.
If the program says "macOS refused access to the receiver", do the same.

### 2. Quick check (under a minute)

Windows:

```powershell
& "$env:USERPROFILE\easyswitch.exe" diag --quick
```

macOS:

```sh
./easyswitch diag --quick
```

What to do: when the program says so, move the mouse and press a key on the
keyboard, so both are awake. Then leave them while it checks the receiver's
slots. It says "Finished." when done.

The last line it prints is the full path of the report, a file named like
`easyswitch-diag-20261112-153000.txt` in the folder you ran it from (your user
folder if you followed the steps above).

If it says "No Logitech receiver found.", plug in the Bolt receiver and try
again.

### 3. Full measurement (about 40 minutes)

This one measures how fast the mouse and keyboard answer after resting for
10 seconds, 1 minute, 5 minutes and 30 minutes. It takes about 40 minutes for
each Logitech receiver plugged into the computer. Start it when you can leave
the computer alone.

Windows:

```powershell
& "$env:USERPROFILE\easyswitch.exe" diag
```

macOS:

```sh
./easyswitch diag
```

What to do, step by step (the program tells you each step and counts down):

1. "Receiver 1 of 1: move the mouse and press a key on the keyboard now, so
   the devices are awake." (the numbers differ if more than one receiver is
   plugged in): do that once.
2. "Do not touch the mouse or the keyboard for ...": **leave both devices
   completely alone** until the program asks for them again. This is the
   important part: a single touch spoils the measurement.
3. "Now move the mouse and press a key on the keyboard (Shift is fine)": do
   that once.
4. "Press a key on the keyboard now": do that once.
5. "Now press the Easy-Switch button on the keyboard to go to another
   channel, wait a few seconds, then press the button for this computer's
   channel to come back": do exactly that.
6. Wait for "Finished." and the path of the report.

Keep the window open the whole time, and make sure the computer does not go
to sleep during these 40 minutes.

### 4. Channel switch recording (about 3 minutes)

This repeats only the last part of the full measurement: the program records
everything the receiver sends while you switch channels by hand.

Windows:

```powershell
& "$env:USERPROFILE\easyswitch.exe" diag --watch 2m --idle=
```

macOS:

```sh
./easyswitch diag --watch 2m --idle=
```

What to do: move the mouse and press a key when asked. When it says "Press a
key on the keyboard now", press one. When it asks you to, press the
Easy-Switch button on the keyboard for another channel, wait a few seconds,
then press the button for this computer's channel to come back. The report
path is printed at the end, as before.

### 5. Boot diag (one restart)

This finds out whether the program can reach the receiver as a system
service while the computer is still at the login screen, before anyone logs
in. That is what the Home Assistant service will need. It installs a
temporary service, which you remove again at the end.

Install it.

Windows: open PowerShell with "Run as administrator" (right-click PowerShell
in the Start menu), then run:

```powershell
& "$env:USERPROFILE\easyswitch.exe" diag --boot-install
```

macOS (asks for your password):

```sh
sudo ./easyswitch diag --boot-install
```

It answers "Installed." Do not move or delete the program file until you have
removed the service again.

Then:

1. Restart the computer.
2. At the login screen, wait one minute while moving the mouse and pressing
   Shift on the keyboard now and then.
3. Log in.

The service measures every 15 seconds for five minutes after the computer
starts, then stops by itself. Save what it measured to a file (a normal,
not administrator, PowerShell or Terminal is fine).

Windows (this copies the service's report file byte for byte; piping
`diag --boot-report` into a file in Windows PowerShell can garble user or
device names with accents or other non-English letters):

```powershell
Copy-Item "$env:ProgramData\easyswitch\boot-diag.txt" "$env:USERPROFILE\easyswitch-boot-report.txt"
```

To just look at the report in the window, run
`& "$env:USERPROFILE\easyswitch.exe" diag --boot-report`.

macOS:

```sh
./easyswitch diag --boot-report > easyswitch-boot-report.txt
```

The report is now in `easyswitch-boot-report.txt` in your user folder. Each
measurement in it says whether it ran "before login" or "logged in". If it
says "No boot report yet" (on Windows, if `Copy-Item` says it cannot find
`boot-diag.txt`), the computer has not restarted since the install.

Then remove the service. This also deletes the service's own copy of the
report, so save it first as above.

Windows, again in a PowerShell opened with "Run as administrator":

```powershell
& "$env:USERPROFILE\easyswitch.exe" diag --boot-remove
```

macOS:

```sh
sudo ./easyswitch diag --boot-remove
```

It answers "Removed." (or "Nothing to remove." if it was already gone).

Known limit on macOS: if FileVault is turned on (System Settings > Privacy &
Security > FileVault), system services start only after the disk is
unlocked, and unlocking the disk at startup also logs you in. On such a Mac
the report may never show a measurement "before login". That is expected and
still a useful result; send the report anyway.

### 6. Try switching

`switch N` moves every Easy-Switch device on the receiver to channel N (1, 2
or 3). Run it on the computer the devices are connected to right now: only
that computer can send them away. For example, to send them to channel 2:

Windows:

```powershell
& "$env:USERPROFILE\easyswitch.exe" switch 2
```

macOS:

```sh
./easyswitch switch 2
```

It looks for devices for up to 8 seconds. Move the mouse or press a key if a
device sleeps; a sleeping device cannot be switched until it wakes. Then it
prints one line per device:

- `left (was on channel 1)`: the device stopped answering on this computer
  (it was on channel 1), which normally means it went to the new channel.
- `already`: it was on that channel already.
- `asleep`: it did not wake up in time and stayed where it was.
- `error: ...`: something went wrong; the text says what.

To get the devices back, press the Easy-Switch button for this computer's
channel on the keyboard (and on the mouse, if it does not follow).

### 7. What to send back

From each computer:

- the report of `diag --quick`,
- the report of `diag`,
- the report of `diag --watch 2m --idle=`,
- `easyswitch-boot-report.txt`,
- the lines `switch` printed (copy them from the window),
- whether Logi Options+ was running during the measurements, and whether it
  kept working normally during and after them (mouse buttons, gestures, its
  own device list),
- which computer it was (Windows or Mac, and which channel it uses), and
  which mouse and keyboard you have.

The reports are plain text. They contain the names of your Logitech devices,
the program version, the operating system, times, and, in the boot report,
the name of the user who was logged in. They contain nothing from your files.

Send them back to whoever asked you to measure, or attach them to an issue
on this repository.

## How it works

Logitech devices that support Easy-Switch understand a command in Logitech's
HID++ protocol called ChangeHost: "go to channel N". easyswitch sends that
command through the Bolt receiver to each device behind it. Some things follow
from that:

- Only the computer the devices are currently connected to can reach them,
  so only that computer can send them to another channel. A computer cannot
  pull devices over from another one.
- A device that sleeps does not hear the command. It can be switched only
  after it wakes up, for example when you move the mouse or press a key.
- The program opens the receiver without taking it away from other programs,
  so Logi Options+ keeps working.

## Credits

easyswitch builds on what [mxswitch](https://github.com/marcocosta97/mxswitch)
by Marco Costa worked out about switching Logitech devices over HID++. Its
test frames and reply rules are used under the MIT License; see
[NOTICE](NOTICE).

## License

MIT, see [LICENSE](LICENSE).
