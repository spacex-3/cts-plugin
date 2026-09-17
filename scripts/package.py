"""Build the archive/checksum pair expected by the CPA plugin store."""
from pathlib import Path
import argparse
import hashlib
import platform
import re
import zipfile

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument('--version', default='0.1.0')
parser.add_argument('--goos', default={'Darwin': 'darwin', 'Windows': 'windows'}.get(platform.system(), 'linux'))
parser.add_argument('--goarch', default={'x86_64': 'amd64', 'AMD64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine(), platform.machine()))
parser.add_argument('--library', type=Path)
args = parser.parse_args()
assert re.fullmatch(r'[0-9][0-9A-Za-z.+-]*', args.version), 'invalid version'
assert args.goos in {'linux', 'darwin', 'windows'}, 'unsupported GOOS'
assert args.goarch in {'amd64', 'arm64'}, 'unsupported GOARCH'
ext = {'linux': 'so', 'darwin': 'dylib', 'windows': 'dll'}[args.goos]
library = args.library or root / 'dist' / f'codex-turn-state.{ext}'
assert library.is_file(), f'missing {library}'
dist = root / 'dist'
dist.mkdir(exist_ok=True)
archive = dist / f'codex-turn-state_{args.version}_{args.goos}_{args.goarch}.zip'
with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as out:
    out.write(library, f'codex-turn-state.{ext}')
    for name in ['README.md', 'README_CN.md', 'LICENSE', 'config.example.yaml']:
        out.write(root / name, name)
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
checksum = f'{digest}  {archive.name}\n'
archive.with_suffix('.zip.sha256').write_text(checksum)
entries = sorted(dist.glob('codex-turn-state_*.zip.sha256'))
(dist / 'checksums.txt').write_text(''.join(p.read_text() for p in entries))
print(archive)
