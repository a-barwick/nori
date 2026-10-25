#!/bin/bash
# Terraform owns cloud lifecycle; gcloud is authentication, readback and IAP only.
set -Eeuo pipefail
umask 077
original_args=("$@")
action=${1:-}; [ "$#" -gt 0 ] && shift
project= disk= state= key= execute=false port=9229
while [ "$#" -gt 0 ]; do
  case "$1" in
    --project) project=${2:?}; shift 2 ;;
    --disk) disk=${2:?}; shift 2 ;;
    --state-dir) state=${2:?}; shift 2 ;;
    --ssh-key) key=${2:?}; shift 2 ;;
    --execute) execute=true; shift ;;
    --port) port=${2:?}; shift 2 ;;
    --) shift; break ;;
    --create-data-disk) echo 'Foundation Terraform owns the retained disk; creation flag retired' >&2; exit 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done
case "$action" in start|stop|status|exec|tunnel|restart-guest) ;; *) echo 'Use start|stop|status|exec|tunnel|restart-guest' >&2; exit 2 ;; esac
[ "$project" = bot-111549 ] && [ "$disk" = bot-computer-data ] || { echo 'Explicit --project bot-111549 --disk bot-computer-data required' >&2; exit 2; }
[ -n "$state" ] || { echo '--state-dir PATH required' >&2; exit 2; }
state=$(python3 -c 'from pathlib import Path; import sys; print(Path(sys.argv[1]).expanduser().resolve())' "$state")
zone=us-central1-a
script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
tf=$script_dir/terraform/tf.sh
# The backend owns one host regardless of --state-dir. Keep preflight, reviewed
# apply, readiness and any EXIT cleanup inside the same shared lifecycle lock.
case "$action" in
  start|stop|restart-guest)
    if ! python3 "$script_dir/lifecycle-lock.py" --held; then
      exec python3 "$script_dir/lifecycle-lock.py" "$script_dir/computer.sh" "${original_args[@]}"
    fi
    ;;
esac
gc=(gcloud --project "$project" --quiet)
ssh_host() { ssh -F "$state/ssh_config" -T nori-host "$@"; }
ssh_guest() { ssh -F "$state/ssh_config" -T nori-guest "$@"; }
tf_runtime() {
  "$tf" runtime "$@"
}
verify_off() {
  local present metadata
  boot=${boot:-$host} # Verified GCE default, also safe if an interrupted read assigned empty.
  present=$("${gc[@]}" compute instances list --zones "$zone" --filter "name=$host" --format 'value(name)') || return $?
  [ -z "$present" ] || { echo "VM still present: $host" >&2; return 1; }
  present=$("${gc[@]}" compute disks list --zones "$zone" --filter "name=$boot" --format 'value(name)') || return $?
  [ -z "$present" ] || { echo "Boot disk still present: $boot" >&2; return 1; }
  metadata=$("${gc[@]}" compute disks describe "$disk" --zone "$zone" --format 'json(name,id,sizeGb,type,users)') || return $?
  python3 -c 'import json,sys; d=json.loads(sys.argv[1]); assert d["id"]=="3757334953750232874" and not d.get("users"), "Original data disk missing/replaced/still attached"; print(json.dumps(d,indent=2))' "$metadata" || return $?
  echo "ABSENT: VM $host and boot disk $boot; original data disk retained/unattached; foundation network retained"
}
runtime_off() {
  tf_runtime plan -input=false -var=enabled=false -out="$state/runtime-off.plan" > "$state/runtime-off-plan.log" || return $?
  tf_runtime apply -input=false "$state/runtime-off.plan" || return $?
  verify_off || return $?
}
set_state() {
  python3 -c 'import json,sys; p=sys.argv[1]; j=json.load(open(p)); j["state"]=sys.argv[2]; open(p,"w").write(json.dumps(j,indent=2)+"\n")' "$state/state.json" "$1"
}
mkdir -p "$state"
chmod 700 "$state"
if [ "$action" = status ]; then
  tf_runtime output -json
  "${gc[@]}" compute instances list --zones "$zone" --filter 'name=nori-computer' --format 'json(name,id,status,scheduling)'
  "${gc[@]}" compute disks describe "$disk" --zone "$zone" --format 'json(name,id,sizeGb,users)'
  exit 0
fi
if [ "$action" != start ]; then
  [ -f "$state/state.json" ] || { echo 'Original operator state directory required' >&2; exit 1; }
  host=$(python3 -c 'import json,sys; j=json.load(open(sys.argv[1])); assert j["project"]=="bot-111549" and j["disk"]=="bot-computer-data"; print(j["host"])' "$state/state.json")
  boot=$(python3 -c 'import json,sys; j=json.load(open(sys.argv[1])); print(j.get("boot_disk",j["host"]))' "$state/state.json")
  if ! "$execute"; then echo "Plan: $action host=$host; Terraform runtime only; data/foundation retained; add --execute"; exit 0; fi
  case "$action" in
    exec)
      if [ "$#" = 0 ]; then exec ssh -F "$state/ssh_config" -tt nori-guest; fi
      remote=$(python3 -c 'import shlex,sys; print(shlex.join(sys.argv[1:]))' "$@")
      ssh_guest "$remote"
      ;;
    tunnel)
      [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1024 ] && [ "$port" -le 65535 ]
      exec ssh -F "$state/ssh_config" -N -L "127.0.0.1:$port:127.0.0.1:9222" nori-guest
      ;;
    restart-guest) ssh_host sudo /usr/local/sbin/nori-vm stop; ssh_host sudo /usr/local/sbin/nori-vm start ;;
    stop)
      present=$("${gc[@]}" compute instances list --zones "$zone" --filter "name=$host" --format 'value(name)')
      if [ -n "$present" ]; then
        python3 -c 'import json,sys; assert json.load(open(sys.argv[1])).get("provisioner")=="terraform", "Refusing non-Terraform live host"' "$state/state.json"
        if ! ssh_host sudo /usr/local/sbin/nori-vm stop; then
          echo 'Guest shutdown failed; runtime OFF was not applied. Retry stop or reconcile before the host DELETE deadline.' >&2
          set_state shutdown_failed
          exit 1
        fi
        if ! ssh_host 'cat /var/log/nori-vm-console.log' > "$state/guest-console.log"; then
          echo 'Could not retrieve guest console diagnostics; continuing Terraform runtime OFF after successful guest shutdown.' >&2
        fi
      fi
      if runtime_off; then set_state stopped; else set_state cleanup_failed; exit 1; fi
      ;;
  esac
  exit 0
fi
[ -n "$key" ] || { echo '--ssh-key PATH required; private key stays local' >&2; exit 2; }
key=$(python3 -c 'from pathlib import Path; import sys; print(Path(sys.argv[1]).expanduser().resolve())' "$key")
python3 -c 'from pathlib import Path; import stat,sys; p=Path(sys.argv[1]); assert p.is_file() and not stat.S_IMODE(p.stat().st_mode)&0o077, "SSH key must exist with owner-only permissions"; assert Path(str(p)+".pub").is_file()' "$key"
ssh-keygen -l -f "$key.pub" >/dev/null
# Refuse to replace an existing runtime, including stale ownership after deadline.
managed=$(tf_runtime output -json | python3 -c 'import json,sys; print(json.load(sys.stdin).get("host_name",{}).get("value") or "")')
[ -z "$managed" ] || { echo 'Terraform runtime exists; stop/reconcile it first' >&2; exit 1; }
present=$("${gc[@]}" compute instances list --zones "$zone" --filter 'name=nori-computer' --format 'value(name)')
[ -z "$present" ] || { echo 'Existing host requires operator reconciliation' >&2; exit 1; }
"${gc[@]}" compute disks describe "$disk" --zone "$zone" --format 'json(id,sizeGb,type,users)' | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["id"]=="3757334953750232874" and not d.get("users") and int(d["sizeGb"])==20 and d["type"].endswith("/pd-standard"), "Refusing attached/replaced/unexpected retained disk"'
if [ -f "$state/state.json" ]; then
  python3 -c 'import json,sys; assert json.load(open(sys.argv[1]))["state"]=="stopped", "Stop/reconcile the previous host first"' "$state/state.json"
fi
host=nori-computer
boot=$host
if ! "$execute"; then
  deadline=$(python3 - <<'DEADLINE'
from datetime import datetime,timedelta,timezone
from zoneinfo import ZoneInfo
now=datetime.now(timezone.utc)
cutoff=now.astimezone(ZoneInfo('America/Los_Angeles')).replace(hour=22,minute=0,second=0,microsecond=0)
if now >= cutoff: raise SystemExit('Daily cutoff passed')
print(min(now+timedelta(minutes=30),cutoff).astimezone(timezone.utc).isoformat(timespec='seconds'))
DEADLINE
)
  tf_runtime plan -input=false -var=enabled=true -var="termination_time=$deadline" -var="public_key_path=$key.pub" -out="$state/runtime-start.plan" > "$state/runtime-start-plan.log"
  python3 - "$state/start-plan.json" "$key" "$deadline" <<'PLAN'
import json,sys
p,key,deadline=sys.argv[1:]
open(p,'w').write(json.dumps(dict(project='bot-111549',disk='bot-computer-data',host='nori-computer',key=key,deadline=deadline),indent=2)+'\n')
PLAN
  echo "Terraform plan saved: $state/runtime-start.plan; host=$host DELETE=$deadline"
  echo 'Review runtime-start-plan.log; start --execute consumes this exact plan without extending its deadline.'
  exit 0
fi
[ -f "$state/runtime-start.plan" ] && [ -f "$state/start-plan.json" ]
deadline=$(python3 - "$state/start-plan.json" "$key" <<'CHECK'
import json,sys
from datetime import datetime,timedelta,timezone
from zoneinfo import ZoneInfo
j=json.load(open(sys.argv[1]));assert j['key']==sys.argv[2] and j['project']=='bot-111549' and j['disk']=='bot-computer-data'
now=datetime.now(timezone.utc);deadline=datetime.fromisoformat(j['deadline']);cutoff=now.astimezone(ZoneInfo('America/Los_Angeles')).replace(hour=22,minute=0,second=0,microsecond=0)
assert now < deadline <= min(now+timedelta(minutes=30),cutoff), 'Reviewed deadline expired/invalid; create a new plan'
print(j['deadline'])
CHECK
)
python3 - "$state/state.json" "$host" "$deadline" <<'STATE'
import json,sys
p,host,deadline=sys.argv[1:]
open(p,'w').write(json.dumps(dict(project='bot-111549',disk='bot-computer-data',host=host,boot_disk=host,deadline=deadline,provisioner='terraform',state='starting'),indent=2)+'\n')
STATE
keep=false
finish_start() {
  status=$?
  trap - EXIT INT TERM
  if ! "$keep"; then
    set +e
    if runtime_off; then set_state stopped; else set_state cleanup_failed; [ "$status" != 0 ] || status=1; fi
  fi
  exit "$status"
}
trap finish_start EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
tf_runtime apply -input=false "$state/runtime-start.plan"
metadata=$("${gc[@]}" compute instances describe "$host" --zone "$zone" --format 'json(id,disks)')
id=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["id"])' "$metadata")
actual_boot=$(python3 -c 'import json,re,sys; d=next(d for d in json.loads(sys.argv[1])["disks"] if d.get("boot")); name=d["source"].rsplit("/",1)[-1]; assert re.fullmatch("[a-z][a-z0-9-]{0,62}",name); print(name)' "$metadata")
boot=$actual_boot
python3 -c 'import json,sys; p=sys.argv[1]; j=json.load(open(p)); j["boot_disk"]=sys.argv[2]; j["instance_id"]=sys.argv[3]; open(p,"w").write(json.dumps(j,indent=2)+"\n")' "$state/state.json" "$boot" "$id"
python3 - "$state" "$key" "$host" "$id" "$disk" <<'CONFIG'
import json,shlex,sys
from pathlib import Path
state,key,host,instance,disk=sys.argv[1:]
q=json.dumps
text=f'''Host nori-host
  HostName {host}
  User hostnori
  HostKeyAlias nori-gce-{instance}
  ProxyCommand gcloud --project bot-111549 --quiet compute start-iap-tunnel {host} 22 --zone us-central1-a --listen-on-stdin
  PermitLocalCommand no
Host nori-guest
  HostName 172.30.0.2
  User bot
  HostKeyAlias bot-111549-guest-{disk}
  ProxyCommand ssh -F {shlex.quote(state+'/ssh_config')} -W %h:%p nori-host
Host *
  IdentityFile {q(key)}
  IdentitiesOnly yes
  ForwardAgent no
  StrictHostKeyChecking accept-new
  UserKnownHostsFile {q(state+'/known_hosts')}
  ExitOnForwardFailure yes
  ConnectTimeout 10
  ServerAliveInterval 15
'''
Path(state,'ssh_config').write_text(text)
Path(state,'ssh_config').chmod(0o600)
CONFIG
end=$((SECONDS+600))
while [ "$SECONDS" -lt "$end" ]; do
  "${gc[@]}" compute instances get-serial-port-output "$host" --zone "$zone" --port 1 --start 0 | tr -d '\r' > "$state/setup-serial.log"
  if grep -q '^NORI_COMPUTER_FAIL' "$state/setup-serial.log"; then tail -40 "$state/setup-serial.log"; exit 1; fi
  if grep -q '^NORI_COMPUTER_HOST_READY' "$state/setup-serial.log"; then
    if ssh_guest 'curl --fail --silent http://127.0.0.1:9222/json/version' > "$state/browser-version.json"; then
      set_state running
      keep=true
      echo "READY: Terraform host=$host DELETE=$deadline; original data disk retained"
      exit 0
    fi
  fi
  sleep 10
done
echo 'Warm guest readiness exceeded10minutes; runtime OFF cleanup, data retained' >&2
exit 1
