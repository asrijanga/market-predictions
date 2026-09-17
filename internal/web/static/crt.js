// CRT screen material: the terminal is drawn to a 2D canvas, uploaded as a
// texture, and then put through a shader that does what a cathode ray tube
// does to an image - barrel distortion, scanlines, phosphor bleed, a little
// colour separation at the edges, and the slow brightness wander of a tube
// that has been on for twenty years.
import * as THREE from 'three';

export const CRT_VERTEX = /* glsl */`
  varying vec2 vUv;
  void main() {
    vUv = uv;
    gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
  }
`;

export const CRT_FRAGMENT = /* glsl */`
  precision highp float;
  uniform sampler2D uScreen;
  uniform float uTime;
  uniform float uPower;      // 0 off, 1 fully warmed up
  uniform float uNoise;      // extra static, raised on errors
  uniform vec3  uPhosphor;
  varying vec2 vUv;

  // Pull the image onto a curved glass surface.
  vec2 barrel(vec2 uv, float amount) {
    vec2 c = uv * 2.0 - 1.0;
    float r2 = dot(c, c);
    c *= 1.0 + amount * r2;
    return c * 0.5 + 0.5;
  }

  float hash(vec2 p) {
    return fract(sin(dot(p, vec2(12.9898, 78.233))) * 43758.5453);
  }

  void main() {
    // The power-on sweep: the picture opens from a horizontal line.
    float open = smoothstep(0.0, 0.6, uPower);
    float band = abs(vUv.y - 0.5);
    if (band > open * 0.5 + 0.001) {
      gl_FragColor = vec4(0.0, 0.0, 0.0, 1.0);
      return;
    }

    vec2 uv = barrel(vUv, 0.055);
    if (uv.x < 0.0 || uv.x > 1.0 || uv.y < 0.0 || uv.y > 1.0) {
      gl_FragColor = vec4(0.0, 0.0, 0.0, 1.0);
      return;
    }

    // Colour separation grows toward the edges of the tube.
    float edge = length(uv - 0.5);
    float shift = 0.0006 + edge * 0.0016;
    float r = texture2D(uScreen, uv + vec2(shift, 0.0)).g;
    float g = texture2D(uScreen, uv).g;
    float b = texture2D(uScreen, uv - vec2(shift, 0.0)).g;
    vec3 signal = vec3(r, g, b);

    // Phosphor colour, with the brightest parts blooming toward white.
    float lum = max(max(signal.r, signal.g), signal.b);
    vec3 col = uPhosphor * signal;
    col += vec3(0.55, 1.0, 0.75) * pow(lum, 3.0) * 0.45;

    // Scanlines and the aperture grille, kept shallow: the point is the
    // texture of a tube, not making the text hard to read.
    float lines = 0.93 + 0.07 * sin(uv.y * 1400.0);
    float grille = 0.97 + 0.03 * sin(uv.x * 2200.0);
    col *= lines * grille;

    // A rolling refresh bar, static, and mains flicker.
    float roll = smoothstep(0.0, 0.08, abs(fract(uv.y + uTime * 0.12) - 0.5));
    col *= 0.97 + 0.03 * roll;
    col += (hash(uv * 900.0 + uTime * 60.0) - 0.5) * (0.022 + uNoise);
    col *= 0.985 + 0.015 * sin(uTime * 27.0);

    // Vignette and the glass reflection across the top left.
    col *= 1.0 - 0.45 * pow(edge, 3.4);
    col += vec3(0.03, 0.055, 0.04) * pow(max(0.0, 1.0 - length(uv - vec2(0.28, 0.82)) * 1.9), 3.0);

    gl_FragColor = vec4(col * uPower, 1.0);
  }
`;

// makeScreenMaterial wires a canvas texture into the CRT shader.
export function makeScreenMaterial(canvas) {
  const texture = new THREE.CanvasTexture(canvas);
  texture.minFilter = THREE.LinearFilter;
  texture.magFilter = THREE.LinearFilter;
  texture.generateMipmaps = false;
  const material = new THREE.ShaderMaterial({
    uniforms: {
      uScreen: { value: texture },
      uTime: { value: 0 },
      uPower: { value: 0 },
      uNoise: { value: 0 },
      uPhosphor: { value: new THREE.Color(0.45, 1.0, 0.62) },
    },
    vertexShader: CRT_VERTEX,
    fragmentShader: CRT_FRAGMENT,
  });
  return { material, texture };
}
