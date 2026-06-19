# Building, flashing, and monitoring the bridge

Firmware for a classic ESP32, built with ESP-IDF v6.x. Run everything from the
`bridge/` directory.

## Source ESP-IDF

`idf.py` only exists after you source ESP-IDF. Do this in every new terminal:

```sh
. $HOME/esp/esp-idf/export.sh
```

Use your own ESP-IDF path. If you set up the `get_idf` alias, just run that.

## Find the serial port

ESP32 plugged in over USB:

- Linux: `/dev/ttyUSB0` (`ls /dev/ttyUSB*`). Permission denied? Add yourself to
  the `dialout` group: `sudo usermod -aG dialout $USER`, then log out and back in.
- macOS: `/dev/tty.usbserial-*` or `/dev/tty.SLAB_USBtoUART` (`ls /dev/tty.*`).
- Windows: `COM3`, `COM4`, ... (Device Manager, under Ports).

Use your port wherever the commands below say `/dev/ttyUSB0`.

## Set the target (first time only)

```sh
idf.py set-target esp32
```

## Build, flash, monitor

Do all three at once:

```sh
idf.py -p /dev/ttyUSB0 flash monitor
```

Or one at a time:

```sh
idf.py build                          # compile
idf.py -p /dev/ttyUSB0 flash          # flash (builds first if needed)
idf.py -p /dev/ttyUSB0 monitor        # watch logs, no reflash
```

Run `idf.py monitor` on its own to watch the logs (WiFi, BLE pushes) without
rebuilding or reflashing. Quit the monitor with `Ctrl-]`.

## Save the logs to a file

The monitor prints to your terminal and saves nothing. To keep a copy:

```sh
idf.py -p /dev/ttyUSB0 monitor 2>&1 | tee monitor.log
```

`monitor.log` is gitignored.

## Erase flash

Wipes everything, including the saved WiFi credentials. The bridge then boots
into its setup access point (see [WIFI-SETUP.md](WIFI-SETUP.md)):

```sh
idf.py -p /dev/ttyUSB0 erase-flash
idf.py -p /dev/ttyUSB0 flash
```

## Notes

- Monitor baud is 115200.
- The first build downloads managed components and needs network. After that it
  builds offline.
- Once it joins WiFi the bridge prints `got ip: <address>`. Register that IP in
  the web UI.
