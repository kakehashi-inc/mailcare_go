import gulp from "gulp";
import { mkdirSync } from "fs";

const FONT_PKG = "node_modules/@material-design-icons/font";
const FONT_DEST = "public/fonts/material-icons";

function setupFonts() {
  mkdirSync(FONT_DEST, { recursive: true });
  return gulp.src(`${FONT_PKG}/*.woff2`, { encoding: false }).pipe(gulp.dest(FONT_DEST));
}

export { setupFonts as "setup-fonts" };
export default setupFonts;
