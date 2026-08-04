var e={amplitude:.5,speed:.65,wavelength:80,rings:2,decay:1,refraction:100,dispersion:.5,shine:.5,trigger:`click`,interval:0},t=12,n=340,r=125e4,i=`#version 300 es
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
}`;function o(){if(typeof document>`u`)return!1;let e=document.createElement(`canvas`),t=e.getContext(`2d`);return!!(t&&typeof t.drawElementImage==`function`&&typeof e.requestPaint==`function`)}function s(e,t){return null}function c(o,c={}){let l={...e,...c},{source:u,content:d,output:f}=o,p=f.getContext(`webgl2`,{alpha:!0,depth:!1,stencil:!1,antialias:!1,premultipliedAlpha:!0,powerPreference:`low-power`});if(!p||p.isContextLost())return null;let m=u.getContext(`2d`),h=u,g=!!(m&&typeof m.drawElementImage==`function`&&typeof h.requestPaint==`function`),_=!1,v=()=>{};g&&(h.onpaint=()=>{try{m.reset(),m.drawElementImage(d,0,0),_=!0,v()}catch{}});function y(e,t){let n=p.createShader(e);if(!n)return s(`Unable to allocate a ripple shader.`);if(p.shaderSource(n,t),p.compileShader(n),!p.getShaderParameter(n,p.COMPILE_STATUS)){let e=p.getShaderInfoLog(n);return p.deleteShader(n),s(`Ripple shader compilation failed.`,e)}return n}let b=y(p.VERTEX_SHADER,i),x=y(p.FRAGMENT_SHADER,a);if(!b||!x)return b&&p.deleteShader(b),x&&p.deleteShader(x),g&&(h.onpaint=null),null;let S=p.createProgram();if(!S)return p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),s(`Unable to allocate the ripple program.`);if(p.attachShader(S,b),p.attachShader(S,x),p.linkProgram(S),!p.getProgramParameter(S,p.LINK_STATUS)){let e=p.getProgramInfoLog(S);return p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),s(`Ripple program linking failed.`,e)}let C={},w=p.getProgramParameter(S,p.ACTIVE_UNIFORMS);for(let e=0;e<w;e++){let t=p.getActiveUniform(S,e);t&&(C[t.name.replace(`[0]`,``)]=p.getUniformLocation(S,t.name))}let T=p.createBuffer(),E=p.createTexture();if(!T||!E)return T&&p.deleteBuffer(T),E&&p.deleteTexture(E),p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),g&&(h.onpaint=null),s(`Unable to allocate ripple GPU resources.`);p.bindBuffer(p.ARRAY_BUFFER,T),p.bufferData(p.ARRAY_BUFFER,new Float32Array([-1,-1,1,-1,-1,1,1,1]),p.STATIC_DRAW),p.enableVertexAttribArray(0),p.vertexAttribPointer(0,2,p.FLOAT,!1,0,0),p.bindTexture(p.TEXTURE_2D,E),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_MIN_FILTER,p.LINEAR),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_MAG_FILTER,p.LINEAR),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_WRAP_S,p.CLAMP_TO_EDGE),p.texParameteri(p.TEXTURE_2D,p.TEXTURE_WRAP_T,p.CLAMP_TO_EDGE),p.texImage2D(p.TEXTURE_2D,0,p.RGBA,1,1,0,p.RGBA,p.UNSIGNED_BYTE,new Uint8Array([0,0,0,0]));let D=1;function O(){let e=Math.max(1,f.clientWidth),t=Math.max(1,f.clientHeight),n=Math.min(window.devicePixelRatio||1,1.5),i=Math.min(n,Math.sqrt(r/Math.max(e*t,1))),a=Math.max(.75,i),o=Math.max(1,Math.round(e*a)),s=Math.max(1,Math.round(t*a));if((f.width!==o||f.height!==s)&&(f.width=o,f.height=s),D=Math.min(1,Math.max(.05,d.clientWidth/Math.max(f.clientWidth,1))),g){let e=Math.max(1,Math.round(u.clientWidth)),t=Math.max(1,Math.round(u.clientHeight)),n=Math.max(1,Math.round(e*a)),r=Math.max(1,Math.round(t*a));(u.width!==n||u.height!==r)&&(u.width=n,u.height=r),h.requestPaint()}}O();function k(){!g||!_||(_=!1,p.bindTexture(p.TEXTURE_2D,E),p.texImage2D(p.TEXTURE_2D,0,p.RGBA,p.RGBA,p.UNSIGNED_BYTE,u))}let A=[],j=new Float32Array(48),M=window.matchMedia(`(prefers-reduced-motion: reduce)`).matches;function N(e,n,r=1){M||(A.length>=t&&A.shift(),A.push({x:e,y:n,age:0,amp:r}),G())}function P(e){let t=Math.hypot(f.clientWidth,f.clientHeight),r=n*Math.max(l.speed,.05),i=l.wavelength*Math.max(l.rings,1)*.5;for(let n=A.length-1;n>=0;n--){let a=A[n];a.age+=e,(a.age*r>t+i*3||Math.exp(-Math.max(l.decay,.05)*a.age)*a.amp<.012)&&A.splice(n,1)}}function F(){k();let e=f.width/Math.max(f.clientWidth,1);p.useProgram(S),p.activeTexture(p.TEXTURE0),p.bindTexture(p.TEXTURE_2D,E),p.uniform1i(C.uContent,0),p.uniform2f(C.uResolution,f.width,f.height);for(let n=0;n<t;n++){let t=A[n];j[n*4]=t?t.x*e:0,j[n*4+1]=t?t.y*e:0,j[n*4+2]=t?t.age:0,j[n*4+3]=t?t.amp*Math.max(l.amplitude,0):0}p.uniform4fv(C.uRipples,j),p.uniform1i(C.uCount,A.length),p.uniform1f(C.uSpeed,n*Math.max(l.speed,.05)*e),p.uniform1f(C.uWavelength,Math.max(l.wavelength,4)*e),p.uniform1f(C.uWidth,Math.max(l.wavelength,4)*Math.max(l.rings,1)*.5*e),p.uniform1f(C.uDecay,Math.max(l.decay,.05)),p.uniform1f(C.uRefraction,Math.max(l.refraction,0)*e),p.uniform1f(C.uDispersion,Math.max(l.dispersion,0)),p.uniform1f(C.uShine,Math.max(l.shine,0)),p.uniform1f(C.uHasContent,+!!g),p.uniform1f(C.uMaxX,D),p.bindFramebuffer(p.FRAMEBUFFER,null),p.viewport(0,0,f.width,f.height),p.drawArrays(p.TRIANGLE_STRIP,0,4)}function ee(){p.bindFramebuffer(p.FRAMEBUFFER,null),p.viewport(0,0,f.width,f.height),g?F():(p.clearColor(0,0,0,0),p.clear(p.COLOR_BUFFER_BIT))}let I=0,L=performance.now(),R=!1,z=!1,B=!1,V=!0,H=0,U=window.matchMedia(`(prefers-reduced-motion: reduce)`);function te(){let e=f.clientWidth,t=f.clientHeight;e<10||t<10||N(e*(.15+Math.random()*.7),t*(.15+Math.random()*.7),.6+Math.random()*.5)}function W(e){if(R)return;if(!V){B=!1;return}let t=Math.min(Math.max((e-L)/1e3,0),1/30);if(L=e,M||(P(t),l.interval>0&&(H+=t,H>=l.interval&&(H=0,te()))),A.length>0)F();else if(ee(),!_&&(l.interval<=0||M)){B=!1;return}I=requestAnimationFrame(W)}function G(){R||B||!V||(B=!0,L=performance.now(),I=requestAnimationFrame(W))}v=G,G();function K(e){let t=f.getBoundingClientRect();return[e.clientX-t.left,e.clientY-t.top]}let q=-1e5,J=-1e5;function Y(e){if(l.trigger===`none`)return;let[t,n]=K(e);N(t,n,1)}function X(e){if(l.trigger!==`hover`)return;let[t,n]=K(e);Math.hypot(t-q,n-J)<56||(q=t,J=n,N(t,n,.3))}d.addEventListener(`pointerdown`,Y,{passive:!0}),d.addEventListener(`pointermove`,X,{passive:!0});function Z(){M=U.matches,M&&(A.length=0),G()}U.addEventListener(`change`,Z);let Q=`ResizeObserver`in window?new ResizeObserver(()=>{O(),G()}):null;Q?.observe(f),Q?.observe(d);let $=`IntersectionObserver`in window?new IntersectionObserver(e=>{V=e[e.length-1]?.isIntersecting??!0,V&&G()}):null;$?.observe(f);function ne(e){e.preventDefault(),R=!0,cancelAnimationFrame(I)}return f.addEventListener(`webglcontextlost`,ne,{passive:!1}),{setOptions(e){Object.entries(e).some(([e,t])=>l[e]!==t)&&(Object.assign(l,e),G())},splash:N,resize(){O(),G()},destroy(){z||(z=!0,R=!0,cancelAnimationFrame(I),d.removeEventListener(`pointerdown`,Y),d.removeEventListener(`pointermove`,X),Q?.disconnect(),$?.disconnect(),U.removeEventListener(`change`,Z),f.removeEventListener(`webglcontextlost`,ne),p.isContextLost()||(p.deleteTexture(E),p.deleteProgram(S),p.deleteShader(b),p.deleteShader(x),p.deleteBuffer(T)),g&&(h.onpaint=null))}}}var l=new WeakSet;function u(e){if(l.has(e))return;l.add(e),e.dataset.enhanced=`true`;let t=e.querySelector(`[data-showcase-tabs]`),n=Array.from(e.querySelectorAll(`[data-showcase-tab]`)),r=Array.from(e.querySelectorAll(`[data-showcase-panel]`)),i=e.querySelector(`[data-ripple-shell]`),a=e.querySelector(`[data-showcase-content]`),s=e.querySelector(`[data-ripple-source]`),u=e.querySelector(`[data-ripple-output]`),d=e.querySelector(`[data-ripple-hint]`),f=e.querySelector(`[data-showcase-dialog]`),p=f?.querySelector(`[data-dialog-image]`),m=f?.querySelector(`[data-dialog-title]`),h=f?.querySelector(`[data-dialog-close]`);if(!t||!i||!a||!s||!u)return;let g=i,_=a,v=s,y=u;v.setAttribute(`layoutsubtree`,`true`),t.setAttribute(`role`,`tablist`);let b=0;function x(e,t=!1){b=(e+n.length)%n.length,n.forEach((e,t)=>{let n=t===b;e.setAttribute(`role`,`tab`),e.setAttribute(`aria-selected`,String(n)),e.setAttribute(`aria-controls`,r[t].id),e.tabIndex=n?0:-1}),r.forEach((e,t)=>{e.hidden=t!==b,e.setAttribute(`role`,`tabpanel`),e.setAttribute(`aria-labelledby`,n[t].id),e.tabIndex=0}),t&&n[b].focus(),requestAnimationFrame(D)}n.forEach((e,t)=>{e.id=`showcase-tab-${t}`,e.addEventListener(`click`,e=>{e.preventDefault(),x(t)}),e.addEventListener(`keydown`,e=>{let t;(e.key===`ArrowRight`||e.key===`ArrowDown`)&&(t=b+1),(e.key===`ArrowLeft`||e.key===`ArrowUp`)&&(t=b-1),e.key===`Home`&&(t=0),e.key===`End`&&(t=n.length-1),t!==void 0&&(e.preventDefault(),x(t,!0))})}),x(0);let S=null;e.querySelectorAll(`[data-zoom-link]`).forEach(e=>{e.addEventListener(`click`,t=>{!f||!p||typeof f.showModal!=`function`||(t.preventDefault(),S=e,p.src=e.dataset.src??e.href,p.alt=e.dataset.alt??`Full-size dbterm screenshot`,p.width=Number(e.dataset.width)||1306,p.height=Number(e.dataset.height)||514,m&&(m.textContent=e.dataset.title??`dbterm product view`),f.showModal())})}),h?.addEventListener(`click`,()=>f?.close()),f?.addEventListener(`click`,e=>{e.target===f&&f.close()}),f?.addEventListener(`close`,()=>S?.focus());let C=document.createElement(`span`);C.hidden=!0,C.dataset.rippleAnchor=``,_.before(C);let w=null,T=!1,E=null;function D(){T&&(g.style.height=`${Math.ceil(_.getBoundingClientRect().height)}px`,w?.resize())}function O(){T&&_.parentElement===v&&C.after(_),T=!1,v.hidden=!0,y.hidden=!0,g.style.removeProperty(`height`),e.dataset.ripple=`fallback`,d&&(d.hidden=!0)}let k=navigator,A=!window.matchMedia(`(prefers-reduced-motion: reduce)`).matches,j=!k.connection?.saveData,M=!window.matchMedia(`(pointer: coarse)`).matches&&(k.deviceMemory===void 0||k.deviceMemory>2),N=!!document.createElement(`canvas`).getContext(`webgl2`);A&&j&&M&&N?(T=o(),T&&(g.style.height=`${Math.ceil(_.getBoundingClientRect().height)}px`,v.hidden=!1,v.append(_)),y.hidden=!1,w=c({source:v,content:_,output:y},{amplitude:.34,speed:.58,wavelength:96,rings:2,decay:1.35,refraction:42,dispersion:.16,shine:.38,trigger:`click`,interval:0}),w?(e.dataset.ripple=T?`html-in-canvas`:`overlay`,d&&(d.hidden=!1),E=new ResizeObserver(D),E.observe(_),_.querySelectorAll(`img`).forEach(e=>e.addEventListener(`load`,D,{once:!0})),requestAnimationFrame(D)):O()):e.dataset.ripple=`fallback`;function P(){E?.disconnect(),w?.destroy(),w=null,O()}document.addEventListener(`astro:before-swap`,P,{once:!0}),window.addEventListener(`pagehide`,P,{once:!0})}var d=new WeakSet,f=`IntersectionObserver`in window?new IntersectionObserver((e,t)=>{e.forEach(e=>{e.isIntersecting&&(t.unobserve(e.target),u(e.target))})},{rootMargin:`500px 0px`}):null;function p(){document.querySelectorAll(`[data-product-showcase]`).forEach(e=>{l.has(e)||d.has(e)||(d.add(e),f?f.observe(e):u(e))})}p(),document.addEventListener(`astro:page-load`,p);