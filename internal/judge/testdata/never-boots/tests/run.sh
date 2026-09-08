#!/bin/sh
# Give the app a moment, then give up. Nothing is reported: the judge has to
# turn silence into a complete set of errors by itself.
wget -q -T 3 -O /dev/null "$DEVDUEL_HEALTH_URL" 2>/dev/null || {
	echo "the app never answered $DEVDUEL_HEALTH_URL" >&2
	exit 1
}
