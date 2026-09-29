/* global window, fetch, location, document */
// The page render.mjs drives: prismarine-viewer's three.js world renderer
// with a sky dome and fog, drawing a world snapshot from the capture bot
// from one camera at a time. Based on the build-battle videos' renderer.
global.THREE = require('three')
const THREE = global.THREE
const { Viewer, Entity } = require('prismarine-viewer/viewer')

const renderer = new THREE.WebGLRenderer({ antialias: true, preserveDrawingBuffer: true })
renderer.setPixelRatio(1)
renderer.setSize(640, 360)
document.body.appendChild(renderer.domElement)

const viewer = new Viewer(renderer)
const scene = viewer.scene
const camera = viewer.camera
camera.near = 0.1
camera.far = 1200

const sky = {
  top: { value: new THREE.Color('#5b9be0') },
  horizon: { value: new THREE.Color('#dde8e6') },
  bottom: { value: new THREE.Color('#dde8e6') },
  sunDir: { value: new THREE.Vector3(0.5, 0.62, 0.6).normalize() },
  sunColor: { value: new THREE.Color('#fff1c9') }
}
const dome = new THREE.Mesh(
  new THREE.SphereGeometry(1000, 48, 24),
  new THREE.ShaderMaterial({
    uniforms: sky,
    side: THREE.BackSide,
    depthWrite: false,
    fog: false,
    vertexShader: 'varying vec3 vDir; void main(){ vDir = normalize(position); gl_Position = projectionMatrix * modelViewMatrix * vec4(position,1.0); }',
    fragmentShader: `uniform vec3 top; uniform vec3 horizon; uniform vec3 bottom; uniform vec3 sunDir; uniform vec3 sunColor; varying vec3 vDir;
      void main(){ vec3 d = normalize(vDir); float h = d.y;
        vec3 c = h > 0.0 ? mix(horizon, top, pow(clamp(h * 1.6, 0.0, 1.0), 0.7)) : mix(horizon, bottom, clamp(-h * 4.0, 0.0, 1.0));
        float s = max(dot(d, normalize(sunDir)), 0.0);
        c += sunColor * (pow(s, 64.0) * 0.3 + pow(s, 8.0) * 0.1);
        gl_FragColor = vec4(c, 1.0); }`
  })
)
dome.renderOrder = -1
scene.add(dome)
scene.background = null
scene.fog = new THREE.Fog('#dde8e6', 60, 160)
viewer.ambientLight.color.set('#fff6ea')
viewer.directionalLight.color.set('#ffe9c8')

// Section meshes arrive from the workers; loading waits until none has
// arrived for a while.
let lastMesh = performance.now()
for (const w of viewer.world.workers) {
  const prev = w.onmessage
  w.onmessage = (ev) => {
    if (ev.data.type === 'geometry') lastMesh = performance.now()
    prev(ev)
  }
}
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

let player = null

// load draws the snapshot at url, and the player standing where the bot
// stood, if the snapshot says so.
async function load (url) {
  const world = await (await fetch(url)).json()
  viewer.setVersion(world.version)
  for (const c of world.columns) viewer.addColumn(c.x, c.z, c.chunk)
  const start = performance.now()
  lastMesh = performance.now()
  while (performance.now() - start < 180000) {
    await sleep(250)
    if (performance.now() - lastMesh > 2500 && performance.now() - start > 3000) break
  }
  if (world.player) {
    const e = new Entity('1.16.4', 'player', scene)
    const skin = new THREE.TextureLoader().load('skin.png')
    skin.magFilter = THREE.NearestFilter
    skin.minFilter = THREE.NearestFilter
    skin.flipY = false
    skin.wrapS = THREE.RepeatWrapping
    skin.wrapT = THREE.RepeatWrapping
    e.mesh.traverse((o) => { if (o.isSkinnedMesh) { o.material.map = skin; o.material.needsUpdate = true } })
    player = e.mesh
    player.position.set(world.player.x, world.player.y, world.player.z)
    scene.add(player)
    await sleep(500)
  }
  return { columns: world.columns.length, ms: Math.round(performance.now() - start) }
}

// shot renders one picture: w×h pixels from cam, in the look's light, and
// returns it as a PNG data URL.
function shot ({ w, h, cam, look }) {
  renderer.setSize(w, h)
  camera.aspect = w / h
  camera.fov = cam.fov
  camera.updateProjectionMatrix()
  camera.position.set(cam.x, cam.y, cam.z)
  camera.up.set(0, 1, 0)
  camera.lookAt(cam.tx, cam.ty, cam.tz)
  dome.position.copy(camera.position)
  sky.top.value.set(look.top)
  sky.horizon.value.set(look.horizon)
  sky.bottom.value.set(look.bottom)
  sky.sunDir.value.set(...look.sun).normalize()
  scene.fog.color.set(look.horizon)
  scene.fog.near = look.fog[0]
  scene.fog.far = look.fog[1]
  viewer.ambientLight.intensity = look.ambient
  viewer.directionalLight.intensity = look.directional
  viewer.directionalLight.position.set(...look.sun).normalize()
  if (player && cam.playerYaw !== undefined) player.rotation.y = cam.playerYaw
  renderer.render(scene, camera)
  return renderer.domElement.toDataURL('image/png')
}

window.thumb = { load, shot, ready: true, params: Object.fromEntries(new URLSearchParams(location.search)) }
