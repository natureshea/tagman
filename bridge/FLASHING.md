# Building, flashing, and monitoring the bridge

Firmware for a classic ESP32, built with **ESP-IDF v6.x**. Run all commands from
the `bridge/` directory.

## 1. Source the ESP-IDF environment

`idf.py` is only available after you source ESP-IDF, and you must do this **once
per new terminal**:

```sh
. $HOME/esp/esp-idf/export.sh
```

(Adjust the path to wherever you installed ESP-IDF. Many setups alias this as
`get_idf` — if so, just run `get_idf`.)

## 2. Find the serial port

With the ESP32 plugged in over USB:

- **Linux:** `/dev/ttyUSB0` (check `ls /dev/ttyUSB*`). If you get a permission
  error, add yourself to the `dialout` group: `sudo usermod -aG dialout $USER`
  (log out/in after).
- **macOS:** `/dev/tty.usbserial-*` or `/dev/tty.SLAB_USBtoUART` (`ls /dev/tty.*`).
- **Windows:** `COM3`, `COM4`, … (Device Manager → Ports).

Substitute your port for `/dev/ttyUSB0` below.

## 3. Set the target (first time only)

```sh
idf.py set-target esp32
```

## 4. Build, flash, monitor

Build, flash, and open the serial monitor in one go:

```sh
idf.py -p /dev/ttyUSB0 flash monitor
```

Individual steps:

```sh
idf.py build                          # compile only
idf.py -p /dev/ttyUSB0 flash          # flash only (builds if needed)
idf.py -p /dev/ttyUSB0 monitor        # attach to the running device — NO reflash
```

Use `idf.py monitor` whenever you just want to watch logs (e.g. WiFi join, BLE
push progress) without rebuilding or reflashing.

**Exit the monitor:** `Ctrl-]`

## Capturing logs to a file

The monitor streams to your terminal but doesn't save anything. To keep a copy:

```sh
idf.py -p /dev/ttyUSB0 monitor 2>&1 | tee monitor.log
```

(`monitor.log` is gitignored.)

## Erasing flash

Wipes everything, including the stored WiFi credentials (NVS) — the bridge then
boots into its setup access point (see [WIFI-SETUP.md](WIFI-SETUP.md)):

```sh
idf.py -p /dev/ttyUSB0 erase-flash
idf.py -p /dev/ttyUSB0 flash
```

## Notes

- Default monitor baud is 115200.
- The first build downloads managed components (`idf_component.yml`); needs
  network. After that, builds are offline.
- After a successful boot the bridge prints `got ip: <address>` once it joins
  WiFi — that IP is what you register in the web UI.
