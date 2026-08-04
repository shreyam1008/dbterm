/**
 * Adapted from Canvas UI's RippleVanilla.ts.
 * Copyright (c) 2026 David Haz.
 * Licensed under the MIT + Commons Clause License Condition v1.0.
 * https://github.com/DavidHDev/canvas-ui/blob/main/src/lib/Ripple/RippleVanilla.ts
 *
 * Local changes: fail-closed shader/program setup, bounded render resolution,
 * optional observer fallbacks, and explicit WebGL context-loss cleanup.
 */

export type RippleTrigger = "click" | "hover" | "none";

export interface RippleOptions {
  /** Capture DOM pixels into the shader. Disable for image-heavy content that the experimental API cannot paint reliably. */
  captureContent?: boolean;
  /** Height of the waves (0 to 3). */
  amplitude?: number;
  /** How fast the rings travel outward. 1 is normal speed. */
  speed?: number;
  /** Distance between wave crests in CSS pixels. */
  wavelength?: number;
  /** Number of crests in each wave train (1 to 8). */
  rings?: number;
  /** How quickly the waves lose energy (higher dies faster). */
  decay?: number;
  /** How strongly the waves bend the page content, in CSS pixels. */
  refraction?: number;
  /** Chromatic dispersion splitting colors along the wave slopes (0 to 1). */
  dispersion?: number;
  /** Intensity of the light glints on the wave crests (0 to 2). */
  shine?: number;
  /** What spawns ripples. */
  trigger?: RippleTrigger;
  /** Seconds between ambient ripples. 0 disables them. */
  interval?: number;
}

export interface RippleElements {
  /** Canvas with a layout subtree that hosts the HTML content. */
  source: HTMLCanvasElement;
  /** The element captured from the source canvas. */
  content: HTMLElement;
  /** Canvas where the WebGL effect is rendered. */
  output: HTMLCanvasElement;
}

export interface RippleInstance {
  setOptions: (options: RippleOptions) => void;
  splash: (x: number, y: number, strength?: number) => void;
  resize: () => void;
  destroy: () => void;
}

const DEFAULTS: Required<RippleOptions> = {
  captureContent: true,
  amplitude: 0.5,
  speed: 0.65,
  wavelength: 80,
  rings: 2,
  decay: 1,
  refraction: 100,
  dispersion: 0.5,
  shine: 0.5,
  trigger: "click",
  interval: 0,
};

const MAX_RIPPLES = 12;
const BASE_SPEED = 340;
const MAX_PIXEL_COUNT = 1_250_000;

type PaintableCanvas = HTMLCanvasElement & {
  onpaint?: (() => void) | null;
  requestPaint?: () => void;
};

type ElementImageContext = CanvasRenderingContext2D & {
  drawElementImage?: (element: Element, x: number, y: number) => void;
};

const VERT = `#version 300 es
precision highp float;
layout(location = 0) in vec2 aPos;
out vec2 vUv;
void main () {
  vUv = aPos * 0.5 + 0.5;
  gl_Position = vec4(aPos, 0.0, 1.0);
}`;

const FRAG = `#version 300 es
precision highp float;
in vec2 vUv;
out vec4 outColor;
uniform sampler2D uContent;
uniform vec2 uResolution;
uniform vec4 uRipples[12];
uniform int uCount;
uniform float uSpeed;
uniform float uWavelength;
uniform float uWidth;
uniform float uDecay;
uniform float uRefraction;
uniform float uDispersion;
uniform float uShine;
uniform float uHasContent;
uniform float uMaxX;

vec4 page (vec2 p) {
  p.x = clamp(p.x, 0.0005, uMaxX - 0.0005);
  p.y = clamp(p.y, 0.0005, 0.9995);
  return texture(uContent, p);
}

void main () {
  vec2 pUv = vec2(vUv.x, 1.0 - vUv.y);
  vec2 frag = pUv * uResolution;
  vec2 grad = vec2(0.0);
  float k = 6.28318530718 / uWavelength;
  float w2 = uWidth * uWidth;

  for (int i = 0; i < 12; i++) {
    if (i >= uCount) break;
    vec4 rp = uRipples[i];
    vec2 dv = frag - rp.xy;
    float r = length(dv);
    float front = uSpeed * rp.z;
    float s = r - front;
    float env = exp(-s * s / w2) * exp(-uDecay * rp.z) * rp.w;
    env *= smoothstep(0.0, 0.08, rp.z);
    env *= inversesqrt(1.0 + front / max(uWavelength, 1.0) * 0.2);
    if (env < 0.0015) continue;
    float dh = (k * cos(s * k) - 2.0 * s / w2 * sin(s * k)) * env;
    grad += dv / max(r, 1.0) * dh * uWavelength * 0.16;
  }

  float g = dot(grad, vec2(-0.55, -0.8));
  float glint = pow(clamp(g * 2.2, 0.0, 1.0), 2.0) * uShine;
  float shade = pow(clamp(-g * 1.6, 0.0, 1.0), 2.0) * uShine * 0.3;

  if (uHasContent < 0.5) {
    float a = clamp(glint * 0.9 + shade * 0.5, 0.0, 0.85);
    outColor = vec4(vec3(glint * 0.9), a);
    return;
  }

  vec2 offs = grad * uRefraction / uResolution;
  vec3 col;
  if (uDispersion > 0.001) {
    float d = uDispersion * 0.35;
    col = vec3(
      page(pUv + offs * (1.0 + d)).r,
      page(pUv + offs).g,
      page(pUv + offs * (1.0 - d)).b
    );
  } else {
    col = page(pUv + offs).rgb;
  }
  col += glint;
  col *= 1.0 - shade;
  outColor = vec4(col, 1.0);
}`;

export function supportsHtmlInCanvas(): boolean {
  if (typeof document === "undefined") return false;
  const probe = document.createElement("canvas") as PaintableCanvas;
  const ctx = probe.getContext("2d") as ElementImageContext | null;
  return Boolean(
    ctx &&
      typeof ctx.drawElementImage === "function" &&
      typeof probe.requestPaint === "function",
  );
}

function reportSetupFailure(message: string, detail?: string | null): null {
  if (import.meta.env.DEV) {
    console.warn(`[dbterm showcase] ${message}`, detail ?? "");
  }
  return null;
}

export function createRipple(
  elements: RippleElements,
  options: RippleOptions = {},
): RippleInstance | null {
  const config = { ...DEFAULTS, ...options };
  const { source, content, output } = elements;

  const gl = output.getContext("webgl2", {
    alpha: true,
    depth: false,
    stencil: false,
    antialias: false,
    premultipliedAlpha: true,
    powerPreference: "low-power",
  });
  if (!gl || gl.isContextLost()) return null;

  const sourceCtx = source.getContext("2d") as ElementImageContext | null;
  const paintable = source as PaintableCanvas;
  const htmlInCanvas = Boolean(
    config.captureContent &&
    sourceCtx &&
      typeof sourceCtx.drawElementImage === "function" &&
      typeof paintable.requestPaint === "function",
  );

  let contentDirty = false;
  let wake = () => {};

  if (htmlInCanvas) {
    paintable.onpaint = () => {
      try {
        sourceCtx!.reset();
        sourceCtx!.drawElementImage!(content, 0, 0);
        contentDirty = true;
        wake();
      } catch {
        // The experimental API may reject a paint while layout is changing.
      }
    };
  }

  function compile(type: number, text: string): WebGLShader | null {
    const shader = gl!.createShader(type);
    if (!shader) return reportSetupFailure("Unable to allocate a ripple shader.");
    gl!.shaderSource(shader, text);
    gl!.compileShader(shader);
    if (!gl!.getShaderParameter(shader, gl!.COMPILE_STATUS)) {
      const detail = gl!.getShaderInfoLog(shader);
      gl!.deleteShader(shader);
      return reportSetupFailure("Ripple shader compilation failed.", detail);
    }
    return shader;
  }

  const vertexShader = compile(gl.VERTEX_SHADER, VERT);
  const fragmentShader = compile(gl.FRAGMENT_SHADER, FRAG);
  if (!vertexShader || !fragmentShader) {
    if (vertexShader) gl.deleteShader(vertexShader);
    if (fragmentShader) gl.deleteShader(fragmentShader);
    if (htmlInCanvas) paintable.onpaint = null;
    return null;
  }

  const program = gl.createProgram();
  if (!program) {
    gl.deleteShader(vertexShader);
    gl.deleteShader(fragmentShader);
    if (htmlInCanvas) paintable.onpaint = null;
    return reportSetupFailure("Unable to allocate the ripple program.");
  }

  gl.attachShader(program, vertexShader);
  gl.attachShader(program, fragmentShader);
  gl.linkProgram(program);
  if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
    const detail = gl.getProgramInfoLog(program);
    gl.deleteProgram(program);
    gl.deleteShader(vertexShader);
    gl.deleteShader(fragmentShader);
    if (htmlInCanvas) paintable.onpaint = null;
    return reportSetupFailure("Ripple program linking failed.", detail);
  }

  const uniforms: Record<string, WebGLUniformLocation | null> = {};
  const uniformCount = gl.getProgramParameter(program, gl.ACTIVE_UNIFORMS) as number;
  for (let i = 0; i < uniformCount; i++) {
    const info = gl.getActiveUniform(program, i);
    if (!info) continue;
    uniforms[info.name.replace("[0]", "")] = gl.getUniformLocation(program, info.name);
  }

  const quad = gl.createBuffer();
  const contentTexture = gl.createTexture();
  if (!quad || !contentTexture) {
    if (quad) gl.deleteBuffer(quad);
    if (contentTexture) gl.deleteTexture(contentTexture);
    gl.deleteProgram(program);
    gl.deleteShader(vertexShader);
    gl.deleteShader(fragmentShader);
    if (htmlInCanvas) paintable.onpaint = null;
    return reportSetupFailure("Unable to allocate ripple GPU resources.");
  }

  gl.bindBuffer(gl.ARRAY_BUFFER, quad);
  gl.bufferData(
    gl.ARRAY_BUFFER,
    new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]),
    gl.STATIC_DRAW,
  );
  gl.enableVertexAttribArray(0);
  gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);

  gl.bindTexture(gl.TEXTURE_2D, contentTexture);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
  gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
  gl.texImage2D(
    gl.TEXTURE_2D,
    0,
    gl.RGBA,
    1,
    1,
    0,
    gl.RGBA,
    gl.UNSIGNED_BYTE,
    new Uint8Array([0, 0, 0, 0]),
  );

  let contentMaxX = 1;

  function syncCanvasSize() {
    const cssWidth = Math.max(1, output.clientWidth);
    const cssHeight = Math.max(1, output.clientHeight);
    const preferredDpr = Math.min(window.devicePixelRatio || 1, 1.5);
    const cappedDpr = Math.min(
      preferredDpr,
      Math.sqrt(MAX_PIXEL_COUNT / Math.max(cssWidth * cssHeight, 1)),
    );
    const dpr = Math.max(0.75, cappedDpr);
    const width = Math.max(1, Math.round(cssWidth * dpr));
    const height = Math.max(1, Math.round(cssHeight * dpr));
    if (output.width !== width || output.height !== height) {
      output.width = width;
      output.height = height;
    }
    contentMaxX = Math.min(
      1,
      Math.max(0.05, content.clientWidth / Math.max(output.clientWidth, 1)),
    );
    if (htmlInCanvas) {
      const sourceWidth = Math.max(1, Math.round(source.clientWidth));
      const sourceHeight = Math.max(1, Math.round(source.clientHeight));
      const targetWidth = Math.max(1, Math.round(sourceWidth * dpr));
      const targetHeight = Math.max(1, Math.round(sourceHeight * dpr));
      if (source.width !== targetWidth || source.height !== targetHeight) {
        source.width = targetWidth;
        source.height = targetHeight;
      }
      paintable.requestPaint!();
    }
  }

  syncCanvasSize();

  function uploadContent() {
    if (!htmlInCanvas || !contentDirty) return;
    contentDirty = false;
    gl!.bindTexture(gl!.TEXTURE_2D, contentTexture);
    gl!.texImage2D(
      gl!.TEXTURE_2D,
      0,
      gl!.RGBA,
      gl!.RGBA,
      gl!.UNSIGNED_BYTE,
      source,
    );
  }

  type Wave = { x: number; y: number; age: number; amp: number };
  const ripples: Wave[] = [];
  const rippleData = new Float32Array(MAX_RIPPLES * 4);
  let reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function splash(x: number, y: number, strength = 1) {
    if (reducedMotion) return;
    if (ripples.length >= MAX_RIPPLES) ripples.shift();
    ripples.push({ x, y, age: 0, amp: strength });
    start();
  }

  function pruneRipples(delta: number) {
    const diagonal = Math.hypot(output.clientWidth, output.clientHeight);
    const speedPx = BASE_SPEED * Math.max(config.speed, 0.05);
    const width = config.wavelength * Math.max(config.rings, 1) * 0.5;
    for (let i = ripples.length - 1; i >= 0; i--) {
      const ripple = ripples[i];
      ripple.age += delta;
      const gone =
        ripple.age * speedPx > diagonal + width * 3 ||
        Math.exp(-Math.max(config.decay, 0.05) * ripple.age) * ripple.amp < 0.012;
      if (gone) ripples.splice(i, 1);
    }
  }

  function render() {
    uploadContent();
    const dpr = output.width / Math.max(output.clientWidth, 1);
    gl!.useProgram(program);
    gl!.activeTexture(gl!.TEXTURE0);
    gl!.bindTexture(gl!.TEXTURE_2D, contentTexture);
    gl!.uniform1i(uniforms.uContent, 0);
    gl!.uniform2f(uniforms.uResolution, output.width, output.height);
    for (let i = 0; i < MAX_RIPPLES; i++) {
      const ripple = ripples[i];
      rippleData[i * 4] = ripple ? ripple.x * dpr : 0;
      rippleData[i * 4 + 1] = ripple ? ripple.y * dpr : 0;
      rippleData[i * 4 + 2] = ripple ? ripple.age : 0;
      rippleData[i * 4 + 3] = ripple
        ? ripple.amp * Math.max(config.amplitude, 0)
        : 0;
    }
    gl!.uniform4fv(uniforms.uRipples, rippleData);
    gl!.uniform1i(uniforms.uCount, ripples.length);
    gl!.uniform1f(uniforms.uSpeed, BASE_SPEED * Math.max(config.speed, 0.05) * dpr);
    gl!.uniform1f(uniforms.uWavelength, Math.max(config.wavelength, 4) * dpr);
    gl!.uniform1f(
      uniforms.uWidth,
      Math.max(config.wavelength, 4) * Math.max(config.rings, 1) * 0.5 * dpr,
    );
    gl!.uniform1f(uniforms.uDecay, Math.max(config.decay, 0.05));
    gl!.uniform1f(uniforms.uRefraction, Math.max(config.refraction, 0) * dpr);
    gl!.uniform1f(uniforms.uDispersion, Math.max(config.dispersion, 0));
    gl!.uniform1f(uniforms.uShine, Math.max(config.shine, 0));
    gl!.uniform1f(uniforms.uHasContent, htmlInCanvas ? 1 : 0);
    gl!.uniform1f(uniforms.uMaxX, contentMaxX);
    gl!.bindFramebuffer(gl!.FRAMEBUFFER, null);
    gl!.viewport(0, 0, output.width, output.height);
    gl!.drawArrays(gl!.TRIANGLE_STRIP, 0, 4);
  }

  function renderIdle() {
    gl!.bindFramebuffer(gl!.FRAMEBUFFER, null);
    gl!.viewport(0, 0, output.width, output.height);
    if (htmlInCanvas) {
      render();
    } else {
      gl!.clearColor(0, 0, 0, 0);
      gl!.clear(gl!.COLOR_BUFFER_BIT);
    }
  }

  let raf = 0;
  let lastTime = performance.now();
  let destroyed = false;
  let disposed = false;
  let running = false;
  let visible = true;
  let ambientTimer = 0;
  const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");

  function spawnAmbient() {
    const width = output.clientWidth;
    const height = output.clientHeight;
    if (width < 10 || height < 10) return;
    splash(
      width * (0.15 + Math.random() * 0.7),
      height * (0.15 + Math.random() * 0.7),
      0.6 + Math.random() * 0.5,
    );
  }

  function frame(now: number) {
    if (destroyed) return;
    if (!visible) {
      running = false;
      return;
    }
    const delta = Math.min(Math.max((now - lastTime) / 1000, 0), 1 / 30);
    lastTime = now;
    if (!reducedMotion) {
      pruneRipples(delta);
      if (config.interval > 0) {
        ambientTimer += delta;
        if (ambientTimer >= config.interval) {
          ambientTimer = 0;
          spawnAmbient();
        }
      }
    }
    if (ripples.length > 0) {
      render();
    } else {
      renderIdle();
      if (!contentDirty && (config.interval <= 0 || reducedMotion)) {
        running = false;
        return;
      }
    }
    raf = requestAnimationFrame(frame);
  }

  function start() {
    if (destroyed || running || !visible) return;
    running = true;
    lastTime = performance.now();
    raf = requestAnimationFrame(frame);
  }

  wake = start;
  start();

  function localPoint(event: PointerEvent): [number, number] {
    const rect = output.getBoundingClientRect();
    return [event.clientX - rect.left, event.clientY - rect.top];
  }

  let hoverX = -1e5;
  let hoverY = -1e5;

  function onPointerDown(event: PointerEvent) {
    if (config.trigger === "none") return;
    const [x, y] = localPoint(event);
    splash(x, y, 1);
  }

  function onPointerMove(event: PointerEvent) {
    if (config.trigger !== "hover") return;
    const [x, y] = localPoint(event);
    if (Math.hypot(x - hoverX, y - hoverY) < 56) return;
    hoverX = x;
    hoverY = y;
    splash(x, y, 0.3);
  }

  content.addEventListener("pointerdown", onPointerDown, { passive: true });
  content.addEventListener("pointermove", onPointerMove, { passive: true });

  function onMotionChange() {
    reducedMotion = motionQuery.matches;
    if (reducedMotion) ripples.length = 0;
    start();
  }
  motionQuery.addEventListener("change", onMotionChange);

  const observer =
    "ResizeObserver" in window
      ? new ResizeObserver(() => {
          syncCanvasSize();
          start();
        })
      : null;
  observer?.observe(output);
  observer?.observe(content);

  const intersection =
    "IntersectionObserver" in window
      ? new IntersectionObserver((entries) => {
          visible = entries[entries.length - 1]?.isIntersecting ?? true;
          if (visible) start();
        })
      : null;
  intersection?.observe(output);

  function onContextLost(event: Event) {
    event.preventDefault();
    destroyed = true;
    cancelAnimationFrame(raf);
  }
  output.addEventListener("webglcontextlost", onContextLost, { passive: false });

  return {
    setOptions(next) {
      const changed = Object.entries(next).some(
        ([key, value]) => config[key as keyof RippleOptions] !== value,
      );
      if (!changed) return;
      Object.assign(config, next);
      start();
    },
    splash,
    resize() {
      syncCanvasSize();
      start();
    },
    destroy() {
      if (disposed) return;
      disposed = true;
      destroyed = true;
      cancelAnimationFrame(raf);
      content.removeEventListener("pointerdown", onPointerDown);
      content.removeEventListener("pointermove", onPointerMove);
      observer?.disconnect();
      intersection?.disconnect();
      motionQuery.removeEventListener("change", onMotionChange);
      output.removeEventListener("webglcontextlost", onContextLost);
      if (!gl.isContextLost()) {
        gl.deleteTexture(contentTexture);
        gl.deleteProgram(program);
        gl.deleteShader(vertexShader);
        gl.deleteShader(fragmentShader);
        gl.deleteBuffer(quad);
      }
      if (htmlInCanvas) paintable.onpaint = null;
    },
  };
}
