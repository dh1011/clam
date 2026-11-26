# Sourced off camera by docs/demo.tape: builds clam, a sample project, and
# loads the shell integration.
go build -o bin/clam . || return
export PATH=$PWD/bin:$PATH
export HOME=/tmp/demo
rm -rf "$HOME" && mkdir -p "$HOME" && cd "$HOME" || return
mkdir weather-cli && cd weather-cli || return

cat > index.js <<'EOF'
import { fetchForecast } from './api.js';

const city = process.argv[2] ?? 'Lisbon';
console.log(await fetchForecast(city));
EOF

cat > api.js <<'EOF'
export async function fetchForecast(city) {
  const res = await fetch(`https://wttr.in/${encodeURIComponent(city)}?format=3`);
  if (!res.ok) throw new Error(`Forecast request failed: ${res.status}`);
  return res.text();
}
EOF

cat > package.json <<'EOF'
{ "name": "weather-cli", "version": "0.1.0", "type": "module", "bin": "index.js" }
EOF

git init -q && git add . && git -c user.name=demo -c user.email=demo@example.com commit -qm init

PS1='\[\e[1;34m\]\w\[\e[0m\] $ '
eval "$(clam init bash)"
