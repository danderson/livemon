# livemon

Liveness monitoring daemon for periodic jobs. Run it as a systemd
service with `ExecStart=livemon daemon`, add `ExecStopPost=livemon
poke <service name> $EXIT_CODE`, and you'll get the last-poked
(i.e. when things last ran) and last-success time at http://:9843.

Note, this is not open source software. Check the license.
