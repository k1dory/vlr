package cascade

// AWGInstallScript is shared by the local and remote provisioners. A working
// preinstalled userspace implementation is also supported by awg-quick.
const AWGInstallScript = `
if ! command -v awg >/dev/null || ! command -v awg-quick >/dev/null ||
   { ! modprobe amneziawg 2>/dev/null && ! command -v amneziawg-go >/dev/null; }; then
  . /etc/os-release
  if [ "$ID" != ubuntu ]; then
    echo "Install awg, awg-quick and amneziawg kernel module (or amneziawg-go) first; automatic installation supports Ubuntu" >&2
    exit 1
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y software-properties-common python3-launchpadlib gnupg2 "linux-headers-$(uname -r)"
  add-apt-repository -y -s ppa:amnezia/ppa
  apt-get update -qq
  apt-get install -y amneziawg
fi
command -v awg >/dev/null
command -v awg-quick >/dev/null
modprobe amneziawg 2>/dev/null || command -v amneziawg-go >/dev/null
`
