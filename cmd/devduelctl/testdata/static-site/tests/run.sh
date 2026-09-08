#!/bin/sh
# The tester waits for the app itself: nothing outside the judge network can
# reach either container.
i=0
until wget -q -O /dev/null "$DEVDUEL_HEALTH_URL" 2>/dev/null; do
	i=$((i + 1))
	if [ "$i" -gt 30 ]; then
		break
	fi
	sleep 1
done

check() {
	if wget -q -O /dev/null "$DEVDUEL_TARGET$1" 2>/dev/null; then
		echo pass
	else
		echo fail
	fi
}

health=$(check /health.txt)
todos=$(check /todos.json)

echo "##DEVDUEL_RESULTS##"
printf '{"schema":"devduel.results/1","results":[{"key":"health","status":"%s","duration_ms":0,"message":""},{"key":"list-todos","status":"%s","duration_ms":0,"message":""}]}\n' "$health" "$todos"
