/* NusaMation web control center — vanilla JS modules, no build step. */
import { bindEvents, syncAppHeight } from "./components/events.js?v=45";
import { boot } from "./components/boot.js?v=45";

bindEvents();
syncAppHeight();
boot();
