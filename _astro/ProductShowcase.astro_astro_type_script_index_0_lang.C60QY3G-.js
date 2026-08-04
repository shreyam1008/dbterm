var e={captureContent:!0,amplitude:.5,speed:.65,wavelength:80,rings:2,decay:1,refraction:100,dispersion:.5,shine:.5,trigger:`click`,interval:0},t=12,n=340,r=125e4,i=`#version 300 es
precision highp float;
layout(location = 0) in vec2 aPos;
out vec2 vUv;
void main () {
  vUv = aPos * 0.5 + 0.5;
  gl_Position = vec4(aPos, 0.0, 1.0);
}`,a=`#version 300 es
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
}`;function o(e,t){return null}function s(s,c={}){let l={...e,...c},{source:u,content:d,output:f}=s,p=f.getContext(`webgl2`,{alpha:!0,depth:!1,stencil:!1,antialias:!1,premultipliedAlpha:!0,powerPreference:`low-power`});if(!p||p.isContextLost())return null;let m=u.getContext(`2d`),h=u,g=!!(l.captureContent&&m&&typeof m.drawElementImage==`function`&&typeof h.requestPaint==`function`),_=!1,v=()=>{};g&&(h.onpaint=()=>{try{m.reset(),m.drawElementImage(d,0,0),_=!0,v()}catch{}});function y(e,t){let n=p.createShader(e);if(!n)return o(`Unable to allocate a ripple shader.`);if(p.shaderSource(n,t),p.compileShader(n),!p.getShaderParameter(n,p.COMPILE_STATUS)){let e=p.getShaderInfoLog(n);return p.deleteShader(n),o(`Ripple shader compilation failed.`,e)}return n}let b=y(p.VERTEX_SHADER,i),x=y(p.FRAGMENT_SHADER,a);if(!b||!x)return b&&p.deleteShader(b),x&&p.deleteShader(x),g&&(h.onpaint=null),null;let S=p.createProgram();if(!S)return p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),o(`Unable to allocate the ripple program.`);if(p.attachShader(S,b),p.attachShader(S,x),p.linkProgram(S),!p.getProgramParameter(S,p.LINK_STATUS)){let e=p.getProgramInfoLog(S);return p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),o(`Ripple program linking failed.`,e)}let C={},w=p.getProgramParameter(S,p.ACTIVE_UNIFORMS);for(let e=0;e<w;e++){let t=p.getActiveUniform(S,e);t&&(C[t.name.replace(`[0]`,``)]=p.getUniformLocation(S,t.name))}let T=p.createBuffer(),E=p.createTexture();if(!T||!E)return T&&p.deleteBuffer(T),E&&p.deleteTexture(E),p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),o(`Unable to allocate ripple GPU resources.`);p.bindBuffer(p.ARRAY_BUFFER,T),p.bufferData(p.ARRAY_BUFFER,new Float32Array([-1,-1,1,-1,-1,1,1,1]),p.STATIC_DRAW),p.enableVertexAttribArray(0),p.vertexAttribPointer(0,2,p.FLOAT,!1,0,0),p.bindTexture(p.TEXTURE_2D,E),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_MIN_FILTER,p.LINEAR),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_MAG_FILTER,p.LINEAR),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_WRAP_S,p.CLAMP_TO_EDGE),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_WRAP_T,p.CLAMP_TO_EDGE),p.texImage2D(p.TEXTURE_2D,0,p.RGBA,1,1,0,p.RGBA,p.UNSIGNED_BYTE,new Uint8Array([0,0,0,0]));let D=1;function O(){let e=Math.max(1,f.clientWidth),t=Math.max(1,f.clientHeight),n=Math.min(window.devicePixelRatio||1,1.5),i=Math.min(n,Math.sqrt(r/Math.max(e*t,1))),a=Math.max(.75,i),o=Math.max(1,Math.round(e*a)),s=Math.max(1,Math.round(t*a));if((f.width!==o||f.height!==s)&&(f.width=o,f.height=s),D=Math.min(1,Math.max(.05,d.clientWidth/Math.max(f.clientWidth,1))),g){let e=Math.max(1,Math.round(u.clientWidth)),t=Math.max(1,Math.round(u.clientHeight)),n=Math.max(1,Math.round(e*a)),r=Math.max(1,Math.round(t*a));(u.width!==n||u.height!==r)&&(u.width=n,u.height=r),h.requestPaint()}}O();function k(){!g||!_||(_=!1,p.bindTexture(p.TEXTURE_2D,E),p.texImage2D(p.TEXTURE_2D,0,p.RGBA,p.RGBA,p.UNSIGNED_BYTE,u))}let A=[],j=new Float32Array(48),M=window.matchMedia(`(prefers-reduced-motion: reduce)`).matches;function N(e,n,r=1){M||(A.length>=t&&A.shift(),A.push({x:e,y:n,age:0,amp:r}),W())}function ee(e){let t=Math.hypot(f.clientWidth,f.clientHeight),r=n*Math.max(l.speed,.05),i=l.wavelength*Math.max(l.rings,1)*.5;for(let n=A.length-1;n>=0;n--){let a=A[n];a.age+=e,(a.age*r>t+i*3||Math.exp(-Math.max(l.decay,.05)*a.age)*a.amp<.012)&&A.splice(n,1)}}function P(){k();let e=f.width/Math.max(f.clientWidth,1);p.useProgram(S),p.activeTexture(p.TEXTURE0),p.bindTexture(p.TEXTURE_2D,E),p.uniform1i(C.uContent,0),p.uniform2f(C.uResolution,f.width,f.height);for(let n=0;n<t;n++){let t=A[n];j[n*4]=t?t.x*e:0,j[n*4+1]=t?t.y*e:0,j[n*4+2]=t?t.age:0,j[n*4+3]=t?t.amp*Math.max(l.amplitude,0):0}p.uniform4fv(C.uRipples,j),p.uniform1i(C.uCount,A.length),p.uniform1f(C.uSpeed,n*Math.max(l.speed,.05)*e),p.uniform1f(C.uWavelength,Math.max(l.wavelength,4)*e),p.uniform1f(C.uWidth,Math.max(l.wavelength,4)*Math.max(l.rings,1)*.5*e),p.uniform1f(C.uDecay,Math.max(l.decay,.05)),p.uniform1f(C.uRefraction,Math.max(l.refraction,0)*e),p.uniform1f(C.uDispersion,Math.max(l.dispersion,0)),p.uniform1f(C.uShine,Math.max(l.shine,0)),p.uniform1f(C.uHasContent,+!!g),p.uniform1f(C.uMaxX,D),p.bindFramebuffer(p.FRAMEBUFFER,null),p.viewport(0,0,f.width,f.height),p.drawArrays(p.TRIANGLE_STRIP,0,4)}function te(){p.bindFramebuffer(p.FRAMEBUFFER,null),p.viewport(0,0,f.width,f.height),g?P():(p.clearColor(0,0,0,0),p.clear(p.COLOR_BUFFER_BIT))}let F=0,I=performance.now(),L=!1,R=!1,z=!1,B=!0,V=0,H=window.matchMedia(`(prefers-reduced-motion: reduce)`);function ne(){let e=f.clientWidth,t=f.clientHeight;e<10||t<10||N(e*(.15+Math.random()*.7),t*(.15+Math.random()*.7),.6+Math.random()*.5)}function U(e){if(L)return;if(!B){z=!1;return}let t=Math.min(Math.max((e-I)/1e3,0),1/30);if(I=e,M||(ee(t),l.interval>0&&(V+=t,V>=l.interval&&(V=0,ne()))),A.length>0)P();else if(te(),!_&&(l.interval<=0||M)){z=!1;return}F=requestAnimationFrame(U)}function W(){L||z||!B||(z=!0,I=performance.now(),F=requestAnimationFrame(U))}v=W,W();function G(e){let t=f.getBoundingClientRect();return[e.clientX-t.left,e.clientY-t.top]}let K=-1e5,q=-1e5;function J(e){if(l.trigger===`none`)return;let[t,n]=G(e);N(t,n,1)}function Y(e){if(l.trigger!==`hover`)return;let[t,n]=G(e);Math.hypot(t-K,n-q)<56||(K=t,q=n,N(t,n,.3))}d.addEventListener(`pointerdown`,J,{passive:!0}),d.addEventListener(`pointermove`,Y,{passive:!0});function X(){M=H.matches,M&&(A.length=0),W()}H.addEventListener(`change`,X);let Z=`ResizeObserver`in window?new ResizeObserver(()=>{O(),W()}):null;Z?.observe(f),Z?.observe(d);let Q=`IntersectionObserver`in window?new IntersectionObserver(e=>{B=e[e.length-1]?.isIntersecting??!0,B&&W()}):null;Q?.observe(f);function $(e){e.preventDefault(),L=!0,cancelAnimationFrame(F)}return f.addEventListener(`webglcontextlost`,$,{passive:!1}),{setOptions(e){Object.entries(e).some(([e,t])=>l[e]!==t)&&(Object.assign(l,e),W())},splash:N,resize(){O(),W()},destroy(){R||(R=!0,L=!0,cancelAnimationFrame(F),d.removeEventListener(`pointerdown`,J),d.removeEventListener(`pointermove`,Y),Z?.disconnect(),Q?.disconnect(),H.removeEventListener(`change`,X),f.removeEventListener(`webglcontextlost`,$),p.isContextLost()||(p.deleteTexture(E),p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),p.deleteBuffer(T)),g&&(h.onpaint=null))}}}var c=new WeakSet;function l(e){if(c.has(e))return;c.add(e),e.dataset.enhanced=`true`;let t=e.querySelector(`[data-showcase-tabs]`),n=Array.from(e.querySelectorAll(`[data-showcase-tab]`)),r=Array.from(e.querySelectorAll(`[data-showcase-panel]`)),i=e.querySelector(`[data-ripple-shell]`),a=e.querySelector(`[data-showcase-content]`),o=e.querySelector(`[data-ripple-source]`),l=e.querySelector(`[data-ripple-output]`),u=e.querySelector(`[data-ripple-hint]`),d=e.querySelector(`[data-showcase-dialog]`),f=d?.querySelector(`[data-dialog-image]`),p=d?.querySelector(`[data-dialog-title]`),m=d?.querySelector(`[data-dialog-close]`);if(!t||!i||!a||!o||!l)return;let h=i,g=a,_=o,v=l;_.setAttribute(`layoutsubtree`,`true`),t.setAttribute(`role`,`tablist`);let y=0;function b(e,t=!1){y=(e+n.length)%n.length,n.forEach((e,t)=>{let n=t===y;e.setAttribute(`role`,`tab`),e.setAttribute(`aria-selected`,String(n)),e.setAttribute(`aria-controls`,r[t].id),e.tabIndex=n?0:-1}),r.forEach((e,t)=>{e.hidden=t!==y,e.setAttribute(`role`,`tabpanel`),e.setAttribute(`aria-labelledby`,n[t].id),e.tabIndex=0}),t&&n[y].focus(),requestAnimationFrame(E)}n.forEach((e,t)=>{e.id=`showcase-tab-${t}`,e.addEventListener(`click`,e=>{e.preventDefault(),b(t)}),e.addEventListener(`keydown`,e=>{let t;(e.key===`ArrowRight`||e.key===`ArrowDown`)&&(t=y+1),(e.key===`ArrowLeft`||e.key===`ArrowUp`)&&(t=y-1),e.key===`Home`&&(t=0),e.key===`End`&&(t=n.length-1),t!==void 0&&(e.preventDefault(),b(t,!0))})}),b(0);let x=null;e.querySelectorAll(`[data-zoom-link]`).forEach(e=>{e.addEventListener(`click`,t=>{!d||!f||typeof d.showModal!=`function`||(t.preventDefault(),x=e,f.src=e.dataset.src??e.href,f.alt=e.dataset.alt??`Full-size dbterm screenshot`,f.width=Number(e.dataset.width)||1306,f.height=Number(e.dataset.height)||514,p&&(p.textContent=e.dataset.title??`dbterm product view`),d.showModal())})}),m?.addEventListener(`click`,()=>d?.close()),d?.addEventListener(`click`,e=>{e.target===d&&d.close()}),d?.addEventListener(`close`,()=>x?.focus());let S=document.createElement(`span`);S.hidden=!0,S.dataset.rippleAnchor=``,g.before(S);let C=null,w=!1,T=null;function E(){w&&(h.style.height=`${Math.ceil(g.getBoundingClientRect().height)}px`,C?.resize())}function D(){w&&g.parentElement===_&&S.after(g),w=!1,_.hidden=!0,v.hidden=!0,h.style.removeProperty(`height`),e.dataset.ripple=`fallback`,u&&(u.hidden=!0)}let O=navigator,k=!window.matchMedia(`(prefers-reduced-motion: reduce)`).matches,A=!O.connection?.saveData,j=!window.matchMedia(`(pointer: coarse)`).matches&&(O.deviceMemory===void 0||O.deviceMemory>2),M=!!document.createElement(`canvas`).getContext(`webgl2`);k&&A&&j&&M?(w=!1,v.hidden=!1,C=s({source:_,content:g,output:v},{amplitude:.34,speed:.58,wavelength:96,rings:2,decay:1.35,refraction:42,dispersion:.16,shine:.38,trigger:`click`,interval:0,captureContent:!1}),C?(e.dataset.ripple=`overlay`,u&&(u.hidden=!1),T=new ResizeObserver(E),T.observe(g),g.querySelectorAll(`img`).forEach(e=>e.addEventListener(`load`,E,{once:!0})),requestAnimationFrame(E)):D()):e.dataset.ripple=`fallback`;function N(){T?.disconnect(),C?.destroy(),C=null,D()}document.addEventListener(`astro:before-swap`,N,{once:!0}),window.addEventListener(`pagehide`,N,{once:!0})}var u=new WeakSet,d=`IntersectionObserver`in window?new IntersectionObserver((e,t)=>{e.forEach(e=>{e.isIntersecting&&(t.unobserve(e.target),l(e.target))})},{rootMargin:`500px 0px`}):null;function f(){document.querySelectorAll(`[data-product-showcase]`).forEach(e=>{c.has(e)||u.has(e)||(u.add(e),d?d.observe(e):l(e))})}f(),document.addEventListener(`astro:page-load`,f);