#!/bin/sh
echo "##DEVDUEL_RESULTS##"
cat <<'JSON'
{"schema":"devduel.results/1","results":[
  {"key":"first","status":"pass","duration_ms":1,"message":""},
  {"key":"second","status":"fail","duration_ms":2,"message":"expected 201, got 200"}
]}
JSON
