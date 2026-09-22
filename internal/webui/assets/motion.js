/* Motion primitives — a spring, momentum projection, and rubber-banding.
 *
 * Why hand-rolled: the board is deliberately build-step-free (see the package
 * doc for webui). Pulling in Motion/Framer would cost a bundler for ~120 lines
 * of physics. The parameters here are Apple's, not ours: `damping` and
 * `response`, converted to the stiffness/damping coefficients the integrator
 * actually wants.
 *
 * Springs rather than CSS transitions because every one of these animations
 * must be interruptible: a card the user re-grabs mid-flight has to follow the
 * finger from where it is on screen, not jump to the end of the interrupted
 * transition. A spring animates from the presentation value by construction.
 */
(function () {
  'use strict';

  const reduceMotion = () =>
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // Apple's conversion: response is the period of the undamped oscillation in
  // seconds, damping ratio is the dimensionless one. A "duration" parameter
  // would be a lie — a spring's settle time falls out of these.
  function coefficients({ damping = 1, response = 0.4 }) {
    const omega = (2 * Math.PI) / Math.max(response, 0.01);
    return { stiffness: omega * omega, damping: 2 * damping * omega };
  }

  /* Animate one or more scalar values toward targets, carrying velocity.
   *
   * values:   { x: 0, y: 0 }  — current values, mutated in place
   * targets:  { x: 100 }      — where each key is heading
   * velocity: { x: 800 }      — px/s handed off from the gesture (§5)
   *
   * Returns a handle with .stop() and .retarget(targets, velocity) so a caller
   * can redirect mid-flight without a velocity discontinuity.
   */
  function spring({ values, targets, velocity = {}, damping = 1, response = 0.4, onUpdate, onComplete }) {
    const keys = Object.keys(targets);
    const co = coefficients({ damping, response });
    const vel = {};
    for (const k of keys) vel[k] = velocity[k] || 0;

    if (reduceMotion()) {
      for (const k of keys) values[k] = targets[k];
      onUpdate && onUpdate(values);
      onComplete && onComplete();
      return { stop() {}, retarget() {} };
    }

    let raf = 0;
    let last = performance.now();
    let done = false;

    // Rest thresholds. Position is in px; velocity is in px/s. Both must be
    // under threshold, or a spring creeping asymptotically never ends.
    const REST_DIST = 0.35;
    const REST_VEL = 1.2;

    function step(now) {
      // Clamp dt: a backgrounded tab produces a huge delta that would explode
      // an explicit integrator. Substep to 1/240s for stability.
      let dt = Math.min((now - last) / 1000, 1 / 30);
      last = now;
      const h = 1 / 240;
      while (dt > 0) {
        const s = Math.min(h, dt);
        dt -= s;
        let settled = true;
        for (const k of keys) {
          const a = -co.stiffness * (values[k] - targets[k]) - co.damping * vel[k];
          vel[k] += a * s;
          values[k] += vel[k] * s;
          if (Math.abs(values[k] - targets[k]) > REST_DIST || Math.abs(vel[k]) > REST_VEL) {
            settled = false;
          }
        }
        if (settled) break;
      }
      let atRest = true;
      for (const k of keys) {
        if (Math.abs(values[k] - targets[k]) > REST_DIST || Math.abs(vel[k]) > REST_VEL) {
          atRest = false;
          break;
        }
      }
      if (atRest) {
        for (const k of keys) {
          values[k] = targets[k];
          vel[k] = 0;
        }
        onUpdate && onUpdate(values);
        done = true;
        onComplete && onComplete();
        return;
      }
      onUpdate && onUpdate(values);
      raf = requestAnimationFrame(step);
    }
    raf = requestAnimationFrame(step);

    return {
      stop() {
        if (!done) cancelAnimationFrame(raf);
        done = true;
      },
      // Redirect without a seam: keep the current position and velocity, just
      // change where we are heading. This is what makes a reversed gesture
      // feel like the same object rather than two animations butting heads.
      retarget(nextTargets, nextVelocity) {
        for (const k of Object.keys(nextTargets)) {
          targets[k] = nextTargets[k];
          if (nextVelocity && nextVelocity[k] !== undefined) vel[k] = nextVelocity[k];
        }
        if (done) {
          done = false;
          last = performance.now();
          raf = requestAnimationFrame(step);
        }
      },
      get running() { return !done; },
    };
  }

  /* Where a flick is going to land, per Apple's exponential-decay projection.
   *
   * Not v²/(2·decel) — that is the constant-deceleration textbook answer. The
   * shipped behaviour uses a decay rate, which is why a scroll keeps drifting
   * for a while instead of stopping dead.
   */
  function project(velocity, decelerationRate = 0.998) {
    return (velocity / 1000) * decelerationRate / (1 - decelerationRate);
  }

  /* Progressive resistance past a boundary (§9).
   *
   * A hard stop reads as "frozen"; diminishing follow reads as "responsive,
   * but there is nothing more here".
   */
  function rubberband(overshoot, dimension, constant = 0.55) {
    const d = Math.max(dimension, 1);
    return (overshoot * d * constant) / (d + constant * Math.abs(overshoot));
  }

  /* Track a short position/velocity history from pointer moves.
   *
   * Velocity at release is the whole ballgame for a momentum throw, and a
   * single last-two-samples estimate is far too noisy — a finger that pauses
   * for one frame before lifting would send the card nowhere.
   */
  function tracker() {
    let samples = [];
    return {
      add(x, y, t) {
        samples.push({ x, y, t });
        if (samples.length > 8) samples.shift();
      },
      velocity() {
        if (samples.length < 2) return { x: 0, y: 0 };
        const last = samples[samples.length - 1];
        // Walk back to a sample old enough to be meaningful, but no older than
        // 100ms — a slower gesture should not inherit a stale flick.
        let first = samples[0];
        for (let i = samples.length - 2; i >= 0; i--) {
          if (last.t - samples[i].t > 60) { first = samples[i]; break; }
        }
        const dt = (last.t - first.t) / 1000;
        if (dt <= 0) return { x: 0, y: 0 };
        return { x: (last.x - first.x) / dt, y: (last.y - first.y) / dt };
      },
      reset() { samples = []; },
    };
  }

  window.KPMotion = { spring, project, rubberband, tracker, reduceMotion };
})();
