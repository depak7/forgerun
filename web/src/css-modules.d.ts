// CSS modules (*.module.css) export their class names.
declare module "*.module.css" {
  const classes: Record<string, string>;
  export default classes;
}
