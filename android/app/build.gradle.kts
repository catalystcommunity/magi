plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "community.catalyst.magi"
    compileSdk = 36

    defaultConfig {
        applicationId = "community.catalyst.magi"
        minSdk = 29
        targetSdk = 36
        // tools/build-android.sh sets these; each build needs a higher code
        // for the self-update (Updater.kt).
        versionCode = (findProperty("magiVersionCode") as String?)?.toInt() ?: 1
        versionName = (findProperty("magiVersionName") as String?) ?: "0.1.0"
    }
    buildTypes {
        getByName("debug") { applicationIdSuffix = ".dev" }
        getByName("release") { isMinifyEnabled = false }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    sourceSets["main"].java.srcDir("../../clients/kotlin")
}

dependencies {
    testImplementation(kotlin("test"))
    testImplementation("org.junit.jupiter:junit-jupiter:5.11.0")
}

tasks.withType<Test>().configureEach { useJUnitPlatform() }
