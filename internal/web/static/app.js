// MKTPREDICT 8000 - a machine that answers one question at a time.
import * as THREE from 'three';
import { Terminal } from './terminal.js';
import { makeScreenMaterial } from './crt.js';
import { PathFan } from './loading.js';
import * as report from './report.js';

const scene = new THREE.Scene();
scene.background = new THREE.Color(0x05040a);
scene.fog = new THREE.Fog(0x05040a, 7, 24);

const camera = new THREE.PerspectiveCamera(42, innerWidth / innerHeight, 0.1, 100);
// Far enough back that the whole machine, the keyboard and the room read as
// one object; the screen is still legible because the tube is large.
const view = { orbit: 8.6, minOrbit: 5.4, maxOrbit: 14 };
camera.position.set(0, 2.2, view.orbit);

const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
renderer.setSize(innerWidth, innerHeight);
renderer.toneMapping = THREE.ACESFilmicToneMapping;
renderer.toneMappingExposure = 1.1;
document.body.appendChild(renderer.domElement);

// ---------------------------------------------------------------- the room

const grid = new THREE.GridHelper(80, 80, 0x1c6f4a, 0x123a2c);
grid.position.y = -1.35;
grid.material.transparent = true;
grid.material.opacity = 0.32;
scene.add(grid);

const desk = new THREE.Mesh(
  new THREE.BoxGeometry(9, 0.22, 4.4),
  new THREE.MeshStandardMaterial({ color: 0x16121b, roughness: 0.75, metalness: 0.1 })
);
desk.position.set(0, -1.24, 0.6);
scene.add(desk);

scene.add(new THREE.AmbientLight(0x3b4657, 1.35));
const keyLight = new THREE.DirectionalLight(0xa8bcdc, 1.05);
keyLight.position.set(-4, 6, 5);
scene.add(keyLight);
const rimLight = new THREE.PointLight(0xff3d9a, 18, 16, 2);
rimLight.position.set(4.5, 1.6, -3.4);
scene.add(rimLight);
// The screen lights the room, so its glow is a light of its own.
const screenLight = new THREE.PointLight(0x46ff9b, 0, 7, 2);
screenLight.position.set(0, 0.55, 1.3);
scene.add(screenLight);

// ------------------------------------------------------------- the monitor

const monitor = new THREE.Group();
scene.add(monitor);

const caseMat = new THREE.MeshStandardMaterial({ color: 0xd6cbb0, roughness: 0.72, metalness: 0.05 });
const darkMat = new THREE.MeshStandardMaterial({ color: 0x191713, roughness: 0.9 });

const shell = new THREE.Mesh(new THREE.BoxGeometry(4.5, 3.5, 3.1), caseMat);
shell.position.z = -0.9;
monitor.add(shell);

// The front bezel is a touch wider, the way moulded plastic cases were.
const bezel = new THREE.Mesh(new THREE.BoxGeometry(4.66, 3.62, 0.34), caseMat);
bezel.position.z = 0.62;
monitor.add(bezel);

const recess = new THREE.Mesh(new THREE.BoxGeometry(3.92, 2.92, 0.12), darkMat);
recess.position.z = 0.76;
monitor.add(recess);

const terminal = new Terminal();
const { material: screenMat, texture: screenTex } = makeScreenMaterial(terminal.canvas);
const screen = new THREE.Mesh(new THREE.PlaneGeometry(3.78, 2.8, 24, 24), screenMat);
// A gentle bulge, because the glass was never flat.
{
  const pos = screen.geometry.attributes.position;
  for (let i = 0; i < pos.count; i++) {
    const x = pos.getX(i) / 1.89, y = pos.getY(i) / 1.4;
    pos.setZ(i, (1 - x * x * 0.45 - y * y * 0.45) * 0.09);
  }
  screen.geometry.computeVertexNormals();
}
screen.position.z = 0.8;
monitor.add(screen);

// Vents, a brand plate and a power lamp.
for (let i = 0; i < 9; i++) {
  const vent = new THREE.Mesh(new THREE.BoxGeometry(2.6, 0.045, 0.05), darkMat);
  vent.position.set(0, 1.86 - i * 0.075, -0.85);
  vent.rotation.x = Math.PI / 2;
  monitor.add(vent);
}
const plate = new THREE.Mesh(new THREE.BoxGeometry(1.5, 0.2, 0.04), darkMat);
plate.position.set(-1.35, -1.58, 0.79);
monitor.add(plate);
const lamp = new THREE.Mesh(
  new THREE.SphereGeometry(0.055, 16, 16),
  new THREE.MeshStandardMaterial({ color: 0x220a06, emissive: 0xff5522, emissiveIntensity: 0 })
);
lamp.position.set(1.72, -1.58, 0.8);
monitor.add(lamp);

const stand = new THREE.Mesh(new THREE.CylinderGeometry(0.55, 0.95, 0.42, 24), caseMat);
stand.position.set(0, -2.0, -0.4);
monitor.add(stand);
monitor.position.y = 0.55;

// ------------------------------------------------------------- the keyboard

const keyboard = new THREE.Group();
const kbBase = new THREE.Mesh(new THREE.BoxGeometry(4.6, 0.22, 1.5), caseMat);
keyboard.add(kbBase);
const keyGeo = new THREE.BoxGeometry(0.2, 0.08, 0.2);
const keyMat = new THREE.MeshStandardMaterial({ color: 0x2b2822, roughness: 0.8 });
const keys = new THREE.InstancedMesh(keyGeo, keyMat, 15 * 5);
{
  const m = new THREE.Matrix4();
  let i = 0;
  for (let row = 0; row < 5; row++) {
    for (let col = 0; col < 15; col++) {
      m.setPosition(-1.85 + col * 0.265 + row * 0.045, 0.15, -0.48 + row * 0.24);
      keys.setMatrixAt(i++, m);
    }
  }
  keys.instanceMatrix.needsUpdate = true;
}
keyboard.add(keys);
keyboard.position.set(0, -1.05, 2.5);
keyboard.rotation.x = -0.06;
scene.add(keyboard);

// ------------------------------------------------------------------- state

const fan = new PathFan();
const proxy = document.getElementById('keyboard-proxy');
const hint = document.getElementById('hint');
const gate = document.getElementById('boot-gate');

const state = {
  mode: 'off',       // off | booting | ready | working
  power: 0,
  input: '',
  stream: null,
  keyFlash: 0,
};

function typeOut(lines, done) {
  let i = 0;
  const tick = () => {
    if (i >= lines.length) { done?.(); return; }
    terminal.write(lines[i++]);
    setTimeout(tick, 70);
  };
  tick();
}

function powerOn() {
  if (state.mode !== 'off') return;
  state.mode = 'booting';
  gate.classList.add('gone');
  lamp.material.emissiveIntensity = 2.4;
  terminal.clear();
  typeOut(report.BOOT, () => {
    state.mode = 'ready';
    terminal.setPrompt('▶ ');
    hint.classList.remove('gone');
  });
  proxy.focus({ preventScroll: true });
}

function submit(symbol) {
  if (state.mode !== 'ready' || !symbol) return;
  const clean = symbol.toUpperCase().replace(/[^A-Z.\-]/g, '');
  if (!clean) return;

  state.mode = 'working';
  state.input = '';
  terminal.setInput('');
  terminal.setPrompt('');
  terminal.clear();
  terminal.write(`\x02 ANALYSING ${clean}`);
  terminal.write('');
  hint.classList.add('gone');

  const nextFan = new PathFan();
  terminal.setOverlay((ctx, canvas, time) => nextFan.draw(ctx, canvas, time));

  const stream = new EventSource(`/api/analyze?symbol=${encodeURIComponent(clean)}`);
  state.stream = stream;

  stream.addEventListener('stage', (e) => {
    const { stage, fraction } = JSON.parse(e.data);
    nextFan.setStage(stage, fraction);
  });
  stream.addEventListener('result', (e) => {
    stream.close();
    state.stream = null;
    finish(report.lines(JSON.parse(e.data)));
  });
  stream.addEventListener('error', (e) => {
    stream.close();
    state.stream = null;
    let message = 'LINK FAILURE - THE SERVER DID NOT ANSWER';
    try { message = JSON.parse(e.data).message; } catch { /* transport error */ }
    screenMat.uniforms.uNoise.value = 0.35;
    setTimeout(() => { screenMat.uniforms.uNoise.value = 0; }, 900);
    finish(['', `\x02 ${message}`, '', '\x01 TRY ANOTHER SYMBOL.']);
  });
}

function finish(lines) {
  terminal.setOverlay(null);
  terminal.clear();
  let i = 0;
  const tick = () => {
    if (i >= lines.length) {
      terminal.write('');
      terminal.setPrompt('▶ ');
      state.mode = 'ready';
      hint.classList.remove('gone');
      return;
    }
    terminal.write(lines[i++]);
    setTimeout(tick, 34);
  };
  tick();
}

// ------------------------------------------------------------------- input

function onKey(e) {
  if (state.mode === 'off') { powerOn(); e.preventDefault(); return; }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  state.keyFlash = 1;

  if (e.key === 'Enter') {
    submit(state.input);
    e.preventDefault();
    return;
  }
  if (state.mode !== 'ready') return;
  if (e.key === 'Backspace') {
    state.input = state.input.slice(0, -1);
  } else if (e.key.length === 1 && /[a-zA-Z.\-]/.test(e.key) && state.input.length < 6) {
    state.input += e.key.toUpperCase();
  } else {
    return;
  }
  terminal.setInput(state.input);
  e.preventDefault();
}

addEventListener('keydown', onKey);
gate.addEventListener('click', powerOn);
renderer.domElement.addEventListener('click', () => {
  if (state.mode === 'off') powerOn();
  else proxy.focus({ preventScroll: true });
});

// Look around by dragging; the view returns to centre when released.
const look = { x: 0, y: 0, tx: 0, ty: 0, dragging: false, px: 0, py: 0 };
renderer.domElement.addEventListener('pointerdown', (e) => {
  look.dragging = true; look.px = e.clientX; look.py = e.clientY;
});
addEventListener('pointerup', () => { look.dragging = false; look.tx = 0; look.ty = 0; });
addEventListener('pointermove', (e) => {
  if (look.dragging) {
    look.tx = THREE.MathUtils.clamp(look.tx + (e.clientX - look.px) * 0.0016, -0.5, 0.5);
    look.ty = THREE.MathUtils.clamp(look.ty - (e.clientY - look.py) * 0.0012, -0.22, 0.32);
    look.px = e.clientX; look.py = e.clientY;
  } else {
    look.tx = (e.clientX / innerWidth - 0.5) * 0.16;
    look.ty = (e.clientY / innerHeight - 0.5) * -0.08;
  }
});

// Scroll to lean in and read the screen, or back out to see the machine.
renderer.domElement.addEventListener('wheel', (e) => {
  view.orbit = THREE.MathUtils.clamp(view.orbit + e.deltaY * 0.004, view.minOrbit, view.maxOrbit);
  e.preventDefault();
}, { passive: false });

addEventListener('resize', () => {
  camera.aspect = innerWidth / innerHeight;
  camera.updateProjectionMatrix();
  renderer.setSize(innerWidth, innerHeight);
});

// ------------------------------------------------------------------- frame

let last = performance.now();
function frame(now) {
  requestAnimationFrame(frame);
  const dt = Math.min(0.05, (now - last) / 1000);
  last = now;

  // The tube warms up rather than snapping on.
  const wanted = state.mode === 'off' ? 0 : 1;
  state.power += (wanted - state.power) * Math.min(1, dt * 1.7);
  screenMat.uniforms.uPower.value = state.power;
  screenMat.uniforms.uTime.value = now / 1000;

  // Blink the cursor only while the machine is waiting for a person.
  terminal.setCursor(state.mode === 'ready' && Math.floor(now / 450) % 2 === 0);
  if (terminal.render(now)) screenTex.needsUpdate = true;

  // The room takes its colour from what the screen is doing.
  const busy = state.mode === 'working' ? 1 : 0;
  screenLight.intensity = state.power * (2.6 + busy * 1.8 + Math.sin(now / 240) * 0.18);
  lamp.material.emissiveIntensity = state.power * (2.2 + busy * 1.6);
  rimLight.intensity = 14 + Math.sin(now / 1400) * 4;

  // Keyboard reacts to typing, and the whole machine breathes.
  state.keyFlash *= 0.9;
  keyboard.position.y = -1.05 - state.keyFlash * 0.012;
  monitor.rotation.y = Math.sin(now / 5200) * 0.02;
  monitor.position.y = 0.55 + Math.sin(now / 3100) * 0.012;

  look.x += (look.tx - look.x) * Math.min(1, dt * 3.2);
  look.y += (look.ty - look.y) * Math.min(1, dt * 3.2);
  camera.position.x = Math.sin(look.x) * view.orbit;
  camera.position.z = Math.cos(look.x) * view.orbit;
  camera.position.y = 2.2 + look.y * 3.4;
  camera.lookAt(0, 0.35, 0.4);

  renderer.render(scene, camera);
}
requestAnimationFrame(frame);
